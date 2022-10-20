#!/usr/bin/env bash
act=$1
cmd="srv_mgr"
log_path="./logs/server.log"

if [ "$act" = "start" ]; then
    if [ ! -d "logs" ];then
        mkdir "logs"
    fi
    if [ ! -f "$cmd" ];then
        go build -o $cmd
    fi

    pid=`pidof $cmd`
    if [ ! "$pid" = "" ]; then
        echo "server already running"
        exit
    fi

    nohup ./$cmd > $log_path 2>&1 &

    pid=`pidof $cmd`
    if [ "$pid" = "" ]; then
        echo "start failed"
        exit
    else
        echo "server pid: $pid"
    fi
    exit
fi
if [ "$act" = "stop" ]; then
    pid=`pidof $cmd`
    if [ "$pid" = "" ]; then
        echo "not found"
        exit
    fi
    echo "killed pid: $pid"
    kill -9 $pid
    exit
fi
if [ "$act" = "rebuild" ]; then
    if [ -f "$cmd" ];then
        current=`date "+%Y-%m-%d_%H:%M:%S"`
        mv ./$cmd ./${cmd}_${current}
    fi
    go build -o $cmd
    exit
fi
if [ "$act" = "restart" ]; then
    pid=`pidof $cmd`
    if [ ! "$pid" = "" ]; then
        kill -10 $(pidof $cmd)
    fi
    exit
fi

echo "unknown action"