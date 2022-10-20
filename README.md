# tcp server hot reload
To keep tcp connections alive when a server is relaoding.

Setp:
1,Father receive a signal to exit.
2,Father forkexec child process.
3,Child connect to father with unix socket pipe.
4,Facher send listener to child,and child rebuild Accept cycle.
5,Father stop Accept cycle.
6,Father send connections to child,and child rebuild connections.
7,Father shutdown unix socket server and exit.
8,Child start a unix socket server.now,child is a new father.
