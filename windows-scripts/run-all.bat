@echo off

ECHO Low Latency Dissent
ECHO ...

ECHO Starting the trustee server ... [might prompt for UAC, please accept]
start trusteeSrv.bat
ping -n 10 127.0.0.1 >nul

ECHO Starting the relay... [might prompt for UAC, please accept]
start relay.bat
ping -n 10 127.0.0.1 >nul

ECHO Starting the client 0... [might prompt for UAC, please accept]
start client0.bat
ping -n 2 127.0.0.1 >nul

ECHO Starting the client 1... [might prompt for UAC, please accept]
start client1.bat