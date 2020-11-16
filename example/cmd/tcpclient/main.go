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
	"os"
	"strings"
	"time"
)

var _dataIn chan []byte

func main() {
	_dataIn = make(chan []byte)

	// test tcp client
	tcpClient := &tcp.TCPClient{
		ServerIP: "192.168.1.36",
		ServerPort: 8080,
		ReceiveHandler: func(data []byte) {
			// log.Print("Client ReceiveHandler: ", string(data))
			select {
			case _dataIn <- data:
				fmt.Println("Received Data Assigned to _dataIn chan []byte")
			default:
				fmt.Println("Received Data Tossed")
			}
		},
		ErrorHandler: func(err error, socketClosedFunc func()) {
			log.Print("Client ErrorHandler: ", err)

			if socketClosedFunc != nil {
				close(_dataIn)
				socketClosedFunc()
				os.Exit(0)
			}
		},
		ReadBufferSize: 1024,
		ReaderYieldDuration: 25 * time.Millisecond,
		ReadDeadLineDuration: 1000 * time.Millisecond,
		WriteDeadLineDuration: 1000 * time.Millisecond,
	}

	if err := tcpClient.Dial(); err != nil {
		close(_dataIn)
		log.Println("Client Dial Error: ", err)
	} else {
		defer close(_dataIn)
		defer tcpClient.Close()

		if err := tcpClient.StartReader(); err != nil {
			log.Println("Start Reader Error: ", err)
		} else {
			/*
			stopLoop := make(chan bool)

			go func() {
				for {
					select {
					case <-stopLoop:
						log.Println("--- LOOP STOPPED ---")
						tcpClient.StopReader()
						log.Println("TCP Client Disconnected")
						return
					default:
						if e := tcpClient.Write([]byte("This is a Test " + time.Now().String())); e != nil {
							log.Println("Write To TCP Server Failed: ", e)
						} else {
							log.Println("Wrote to TCP Server OK")
						}
					}

					time.Sleep(1000 * time.Millisecond)
				}
			}()

			log.Println("TCP Client Dial OK")
			log.Println("Press Any Key To Disconnect Client...")

			_, _ = fmt.Scanln()

			//stopLoop <- true
			tcpClient.StopReader()
			*/

			for {
				fmt.Println("TCP Client Operations:")
				fmt.Println("1 = Send Request, Await Response 3 Seconds")
				fmt.Println("X = Disconnect Client")

				choice := ""
				if _, err := fmt.Scanln(&choice); err == nil {
					choice = util.RightTrimLF(choice)

					if strings.ToUpper(choice) == "X" {
						close(_dataIn)
						tcpClient.StopReader()
						break
					} else if choice == "1" {
						// send request out
						if err := tcpClient.Write([]byte("ClientTestXYZ")); err != nil {
							fmt.Println("Send Data Error: " + err.Error())
							fmt.Println()
							fmt.Println()
						} else {
							// await response from TCP server within 3 seconds
							fmt.Println("Waiting for TCP Server Data In...")

							var recv []byte
							timeOut := false

							select {
							case recv = <-_dataIn:
							case <-time.After(3 * time.Second):
								timeOut = true
							}

							if !timeOut {
								fmt.Println("Data Received: ", string(recv))
							} else {
								fmt.Println("Wait for TCP Server 3 Seconds Time-Out")
							}

							fmt.Println()
							fmt.Println()
						}
					} else {
						fmt.Println()
						fmt.Println()
					}
				} else {
					fmt.Println()
					fmt.Println()
				}
			}
		}
	}
}
