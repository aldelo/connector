package main

/*
 * Copyright 2020 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import (
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/tcp"
	"log"
	"strings"
	"time"
)

func main() {
	// test tcp server
	tcpServer := &tcp.TCPServer{
		Port: 8080,
		ListenerAcceptHandler: func(clientIP string) {
			log.Println("ListenerAcceptHandler: Client Accepted = ", clientIP)
		},
		ListenerErrorHandler: func(err error) {
			log.Print("ListenerErrorHandler: ", err)
		},
		ClientReceiveHandler: func(clientIP string, data []byte, writeToClientFunc func(writeData []byte, clientIP string) error) {
			log.Println("ClientReceiveHandler: ", clientIP, string(data))

			/*
			if e := writeToClientFunc([]byte("TCP ACK Data Received: " + string(data)), clientIP); e != nil {
				log.Println("TCP Server Send Data to ClientIP Failed: ", clientIP, e)
			} else {
				log.Println("TCP Server Send Data to ClientIP OK: ", clientIP)
			}

			 */
		},
		ClientErrorHandler: func(clientIP string, err error) {
			log.Println("ClientErrorHandler: ", clientIP, err)
		},
		ReadBufferSize: 1024,
		ListenerYieldDuration: 25 * time.Millisecond,
		ReaderYieldDuration: 25 * time.Millisecond,
		ReadDeadlineDuration: 0, //1000 * time.Millisecond,
		WriteDeadLineDuration: 1000 * time.Millisecond,
	}

	if err := tcpServer.Serve(); err != nil {
		log.Println(err)
	} else {
		for {
			fmt.Println("TCP Server Action...")
			fmt.Println("1 = Send Data to Client")
			fmt.Println("2 = Disconnect Client")
			fmt.Println("X = Stop TCP Server")

			action := ""
			_, _ = fmt.Scanln(&action)
			action = strings.ToLower(util.RightTrimLF(action))

			if action == "x" {
				// stop tcp server
				tcpServer.Close()
				fmt.Println("TCP Server Shutdown")
				return
			} else if action == "1" {
				// send data to client
				if clientsList := tcpServer.GetConnectedClients(); len(clientsList) > 0 {
					for _, v := range clientsList {
						if e := tcpServer.WriteToClient([]byte("Server to Client Test Data"), v); e != nil {
							log.Println("TCP Server Write Data to Client Failed: ", v, e)
						} else {
							log.Println("TCP Server Write Data to Client OK: ", v)
						}
					}
				}
			} else if action == "2" {
				// disconnect client
				if clientsList := tcpServer.GetConnectedClients(); len(clientsList) > 0 {
					for _, v := range clientsList {
						log.Println("Disconnecting Client IP: ", v)

						tcpServer.DisconnectClient(v)

						log.Println("TCP Client IP Disconnect: ", v)
					}
				}
			}
		}
	}
}
