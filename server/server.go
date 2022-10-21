package server

import (
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var _debug = false
var _role string

type nonBlookLocker struct {
	l1     sync.Mutex
	l2     sync.Mutex
	locked bool
}

type SrvMgr struct {
	wg     sync.WaitGroup
	connWg sync.WaitGroup

	stopAcceptCycle bool

	stopConnCycle bool

	listenAddr string

	listener net.Listener

	sockFile string

	childUnixConn *net.UnixConn

	stopSockServer bool

	connSendLock nonBlookLocker
}

var sig chan os.Signal

const env = "_SERVER_HOT_RESTART_"

func NewMgr() *SrvMgr {
	s := &SrvMgr{
		listenAddr:      "0.0.0.0:9999",
		sockFile:        "/tmp/hot_reload.sock",
		stopAcceptCycle: false,
		stopConnCycle:   false,
		connSendLock:    nonBlookLocker{},
	}

	if os.Getenv(env) == "" {
		_role = "father"
	} else {
		_role = "child"
	}

	return s
}
func (s *SrvMgr) Run() {
	if _role == "father" {
		ln, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			panic(err)
		}
		s.listener = ln
		s.startAccept()
		err := os.Setenv(env, "1")
		if err != nil {
			panic(err)
		}
	} else {
		//接收listener和conn,阻塞运行
		s.startSockClient()
	}
	time.Sleep(time.Second * 2)
	s.signalHandler()
	s.startSockServer()

	myprint("server started")
	_role = "father"
	s.wg.Wait()
	myprint("server exited")
}

func (s *SrvMgr) startAccept() {
	s.wg.Add(1)
	s.stopAcceptCycle = false
	go func() {
		defer s.wg.Done()
		defer myprint("stop accept")

		myprint("start accept")
		for !s.stopAcceptCycle {
			conn, err := s.listener.Accept()
			if err != nil {
				if strings.Contains(err.Error(), "i/o timeout") {
					break
				} else {
					myprint("connect error:", err.Error())
					continue
				}
			}
			s.newConn(conn)
		}
		//cancel deadline
		err := s.listener.(*net.TCPListener).SetDeadline(time.Time{})
		if err != nil {
			panic(err)
		}
	}()
}
func (s *SrvMgr) stopAccept() {
	s.stopAcceptCycle = true
	err := s.listener.(*net.TCPListener).SetDeadline(time.Now())
	if err != nil {
		panic(err)
	}
}

func (s *SrvMgr) newConn(conn net.Conn) {
	s.connWg.Add(1)
	go func() {
		defer s.connWg.Done()
		tryTime := time.Now().Unix()
		maxTryTimes := 10 //max try times
		for {
			if s.stopConnCycle {
				if time.Now().Unix()-tryTime > 1 {
					locked, res := s.sendConn(conn)
					if locked {
						maxTryTimes--
						tryTime = time.Now().Unix()
						if res {
							//sucess
							break
						} else {
							if maxTryTimes <= 0 {
								//force exit
								myprint("force exit conn cycle")
								break
							}
						}
					}
				}
			}

			err := conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if err != nil {
				myprint("conn set deadline error:", err)
			}

			//your business here
			str, err := __recv(&conn)
			if err != nil {
				errMsg := err.Error()
				if strings.Contains(errMsg, "EOF") {
					//client thread exit
					break
				} else if strings.Contains(errMsg, "i/o timeout") {
					//timeout
					continue
				} else {
					myprint("read error:", err)
					continue
				}
			}
			err = __send(&conn, []byte(str))
			if err != nil {
				myprint("__send error:", err)
			}
		}

	}()
}

func (s *SrvMgr) signalHandler() {
	myprint("signalHandler started")
	go func() {
		sig = make(chan os.Signal)
		signal.Notify(sig, syscall.SIGUSR1)

		for {
			s := <-sig
			switch s {
			case syscall.SIGUSR1:
				debug("get SIGUSR1")
				env := &syscall.ProcAttr{
					Env:   os.Environ(),
					Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()},
					Sys:   nil,
				}
				pid, err := syscall.ForkExec(os.Args[0], os.Args, env)
				if err != nil {
					myprint("ForkExec error:" + err.Error())
				} else {
					myprint("forked child pid: ", pid)
				}
			}
		}
	}()
}

func (s *SrvMgr) sendConn(conn net.Conn) (locked, ret bool) {
	//for "to many open files"
	if !s.connSendLock.lock() {
		return
	}
	defer s.connSendLock.Unlock()
	locked = true

	defer func() {
		if r := recover(); r != nil {
			myprint("send conn recover:", r)
		}
	}()

	tcpConn := conn.(*net.TCPConn)
	f, err := tcpConn.File()
	if err != nil {
		myprint("trans conn to file failed:", err)
		ret = false
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			myprint(err)
		}
	}(f)

	err = sendFd(s.childUnixConn, f)
	if err != nil {
		myprint("trans conn failed:", err)
		ret = false
	}
	_, msg, _, err := recv(s.childUnixConn)
	if err != nil {
		myprint("get trans conn resp error:", err)
		myprint(err)
	}
	if msg == "trans_conn_ok" {
		debug("send a conn success")
		ret = true
	} else {
		debug("send a conn failed")
		ret = false
	}
	time.Sleep(time.Microsecond * 10)
	return
}
func (s *SrvMgr) sendListener() bool {
	defer func() {
		if r := recover(); r != nil {
			myprint("send listener recover:", r)
		}
	}()
	tcpln := s.listener.(*net.TCPListener)
	f, err := tcpln.File()
	if err != nil {
		myprint("lintener to TCPListener error:", err.Error())
		return false
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			myprint(err)
		}
	}(f)

	err = sendFd(s.childUnixConn, f)
	if err != nil {
		myprint("send listener error1:", err.Error())
		return false
	}
	_, msg, _, err := recv(s.childUnixConn)
	if err != nil {
		myprint("send listener error2:", err.Error())
		return false
	}
	if msg == "trans_listener_ok" {
		return true
	} else {
		myprint("send listener error3")
		return false
	}
}

func (s *SrvMgr) startSockServer() {
	s.wg.Add(1)
	go func() {
		defer debug("sock server exit")
		defer s.wg.Done()
		err := os.Remove(s.sockFile)
		if err != nil {
			panic(err)
		}

		var unixAddr *net.UnixAddr
		unixAddr, _ = net.ResolveUnixAddr("unix", s.sockFile)
		unixListener, err := net.ListenUnix("unix", unixAddr)
		if err != nil {
			panic(err)
		}

		defer func(unixListener *net.UnixListener) {
			err := unixListener.Close()
			if err != nil {
				myprint(err)
			}
		}(unixListener)

		for {
			err := unixListener.SetDeadline(time.Now().Add(time.Second * 3))
			if err != nil {
				panic(err)
			}
			unixConn, err := unixListener.AcceptUnix()
			if err != nil {
				if strings.Contains(err.Error(), "i/o timeout") {
					if s.stopSockServer {
						break
					} else {
						continue
					}
				}
				myprint("unix sock server Accept error: ", err.Error())
				continue
			}
			s.childUnixConn = unixConn
			//1-> send listener
			_ = sendTxt(unixConn, "trans_listener")

			if !s.sendListener() {
				err := unixConn.Close()
				if err != nil {
					panic(err)
				}
				break
			}
			s.stopAccept()
			time.Sleep(time.Microsecond * 100)

			_ = sendTxt(unixConn, "trans_conn")

			time.Sleep(time.Microsecond * 100)

			s.stopConnCycle = true
			s.connWg.Wait()

			_ = sendTxt(unixConn, "trans_conn_finish")
			time.Sleep(time.Microsecond * 100)

			_ = sendTxt(unixConn, "all_finish")
			time.Sleep(time.Microsecond * 100)
			_ = unixConn.Close()
			myprint("father unix socket exit")
			break
		}
	}()
}

func (s *SrvMgr) startSockClient() {
	var unixAddr *net.UnixAddr
	unixAddr, _ = net.ResolveUnixAddr("unix", s.sockFile)

	unixConn, err := net.DialUnix("unix", nil, unixAddr)
	if err != nil {
		panic(err)
	}

	defer func(unixConn *net.UnixConn) {
		err := unixConn.Close()
		if err != nil {
			myprint(err)
		}
	}(unixConn)

WAIT_CMD:
	for {
		_type, msg, _, err := recv(unixConn)
		if err != nil {
			myprint("wait cmd error:" + err.Error())
			continue
		}
		if _type != "txt" {
			myprint("cmd type error:", _type)
			continue
		}
		switch msg {
		case "trans_listener":
			_type, _, file, err := recv(unixConn)
			if err != nil {
				myprint("trans_listener1", err.Error())
				_ = sendTxt(unixConn, "error")
				continue WAIT_CMD
			}
			if _type != "fd" {
				myprint("trans_listener2")
				_ = sendTxt(unixConn, "trans_listener_error")
				continue WAIT_CMD
			}

			ln, err := net.FileListener(file)
			if err != nil {
				myprint("trans_listener3", err.Error())
				_ = sendTxt(unixConn, "trans_listener_error")
				continue WAIT_CMD
			}
			s.listener = ln
			s.startAccept()
			_ = sendTxt(unixConn, "trans_listener_ok")
			myprint("rebuild listener success")
		case "trans_conn":
			myprint("start trans conn")
			for {
				//wait for:conn fd
				_type, msg, file, err := recv(unixConn)
				if err != nil {
					myprint("recv conn error1:", err.Error())
					_ = sendTxt(unixConn, "trans_conn_error")
					continue
				}
				if _type == "txt" {
					if msg == "trans_conn_finish" {
						break
					} else {
						myprint("recv conn error2:", msg)
						_ = sendTxt(unixConn, "trans_conn_error")
						continue
					}
				}
				if _type == "fd" {
					if s.rebuildConn(file) {
						_ = sendTxt(unixConn, "trans_conn_ok")
					} else {
						_ = sendTxt(unixConn, "trans_conn_error")
					}
				}
			}
			myprint("trans conn finished")
		case "all_finish":
			myprint("child unix socket exit")
			break WAIT_CMD
		}
	}
}

func (s *SrvMgr) rebuildConn(file *os.File) (ret bool) {
	ret = false
	defer func() {
		if r := recover(); r != nil {
			debug("rebuild conn recover:", r)
		}
	}()

	oldConn, err := net.FileConn(file)
	if err != nil {
		myprint("fd to conn error:", err.Error())
		ret = false
	}
	s.newConn(oldConn)
	ret = true
	return
}

func _recv_fd(via *net.UnixConn, num int, filenames []string) ([]*os.File, error) {
	if num < 1 {
		return nil, nil
	}

	// get the underlying socket
	viaf, err := via.File()
	if err != nil {
		return nil, err
	}
	socket := int(viaf.Fd())
	defer func(viaf *os.File) {
		err := viaf.Close()
		if err != nil {
			myprint(err)
		}
	}(viaf)

	// recvmsg
	buf := make([]byte, syscall.CmsgSpace(num*4))
	_, _, _, _, err = syscall.Recvmsg(socket, nil, buf, 0)
	if err != nil {
		return nil, err
	}

	// parse control msgs
	var msgs []syscall.SocketControlMessage
	msgs, err = syscall.ParseSocketControlMessage(buf)

	// convert fds to files
	res := make([]*os.File, 0, len(msgs))
	for i := 0; i < len(msgs) && err == nil; i++ {
		var fds []int
		fds, err = syscall.ParseUnixRights(&msgs[i])

		for fi, fd := range fds {
			var filename string
			if fi < len(filenames) {
				filename = filenames[fi]
			}

			res = append(res, os.NewFile(uintptr(fd), filename))
		}
	}

	return res, err
}
func _send_fd(via *net.UnixConn, files ...*os.File) error {
	if len(files) == 0 {
		return nil
	}

	viaf, err := via.File()
	if err != nil {
		return err
	}
	socket := int(viaf.Fd())
	defer func(viaf *os.File) {
		err := viaf.Close()
		if err != nil {
			myprint(err)
		}
	}(viaf)

	fds := make([]int, len(files))
	for i := range files {
		fds[i] = int(files[i].Fd())
	}

	rights := syscall.UnixRights(fds...)
	return syscall.Sendmsg(socket, nil, rights, nil, 0)
}

func _send_txt(uc *net.UnixConn, b []byte) error {
	l := uint8(len(b))
	_b := []byte{l}
	data := append(_b, b...)
	_, err := uc.Write(data)
	return err
}
func _recv_txt(uc *net.UnixConn) (string, error) {
	b := make([]byte, 1)
	_, err := uc.Read(b)
	if err != nil {
		return "", err
	}
	l := uint8(b[0])
	c := make([]byte, int(l))
	_, err = uc.Read(c)
	return string(c), err
}

func sendFd(uc *net.UnixConn, file *os.File) error {
	err := _send_txt(uc, []byte("fd"))
	if err != nil {
		return err
	}
	err = _send_fd(uc, file)
	if err != nil {
		return err
	}
	return nil
}
func sendTxt(uc *net.UnixConn, str string) error {
	debug("sendTxt", str)
	err := _send_txt(uc, []byte("txt"))
	if err != nil {
		return err
	}
	err = _send_txt(uc, []byte(str))
	if err != nil {
		return err
	}
	return nil
}
func recv(uc *net.UnixConn) (_type string, txt string, file *os.File, err error) {
	_type = ""
	txt = ""
	file = nil

	s, err := _recv_txt(uc)
	if err != nil {
		_type = ""
		txt = ""
		file = nil
	}
	if s == "txt" {
		_type = "txt"
		txt, err = _recv_txt(uc)
		file = nil
		debug("recv_txt", txt)
	}
	if s == "fd" {
		_type = "fd"
		txt = ""
		files, err := _recv_fd(uc, 1, nil)
		if err != nil {
			file = nil
		} else {
			file = files[0]
		}
	}
	return
}

func myprint(a ...interface{}) {
	pid := os.Getpid()
	str := "[pid:" + strconv.Itoa(pid) + "][" + _role + "]"

	_, file, line, _ := runtime.Caller(1)
	info := file + ":" + strconv.Itoa(line)

	v := append([]interface{}{str}, a...)
	v = append([]interface{}{info}, v...)

	log.Println(v...)
}
func debug(a ...interface{}) {
	if _debug {
		pid := os.Getpid()
		str := "[pid:" + strconv.Itoa(pid) + "][" + _role + "]"

		_, file, line, _ := runtime.Caller(1)
		info := file + ":" + strconv.Itoa(line)

		v := append([]interface{}{str}, a...)
		v = append([]interface{}{info}, v...)

		log.Println(v...)
	}
}

func __send(conn *net.Conn, b []byte) error {
	l := uint8(len(b))
	_b := []byte{l}
	data := append(_b, b...)
	_, err := (*conn).Write(data)
	return err
}
func __recv(conn *net.Conn) (string, error) {
	b := make([]byte, 1)
	_, err := (*conn).Read(b)
	if err != nil {
		return "", err
	}
	l := uint8(b[0])
	c := make([]byte, int(l))
	_, err = (*conn).Read(c)
	if err != nil {
		return "", err
	}
	return string(c), nil
}

func (l *nonBlookLocker) lock() (success bool) {
	l.l1.Lock()
	defer l.l1.Unlock()

	if !l.locked {
		l.locked = true
		success = true
		l.l2.Lock()
	}
	return
}
func (l *nonBlookLocker) Unlock() {
	l.l1.Lock()
	defer l.l1.Unlock()

	l.locked = false
	l.l2.Unlock()
}
