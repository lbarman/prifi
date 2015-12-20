package relay

import (
	"encoding/binary"
	"fmt"
	"github.com/lbarman/prifi/config"
	"github.com/lbarman/crypto/abstract"
	"time"
	"log"
	"net"
	"strconv"
	prifinet "github.com/lbarman/prifi/net"
	prifilog "github.com/lbarman/prifi/log"
)

var relayState 			*RelayState 
var stateMachineLogger 	*prifilog.StateMachineLogger

var	protocolFailed        = make(chan bool)
var	indicateEndOfProtocol = make(chan int)
var	deconnectedClients	  = make(chan int)
var	timedOutClients   	  = make(chan int)
var	deconnectedTrustees	  = make(chan int)

func StartRelay(payloadLength int, relayPort string, nClients int, nTrustees int, trusteesIp []string, reportingLimit int, useUDP bool) {

	prifilog.SimpleStringDump(prifilog.NOTIFICATION, "Relay started")

	stateMachineLogger = prifilog.NewStateMachineLogger("relay")
	stateMachineLogger.StateChange("relay-init")

	relayState = initiateRelayState(relayPort, nTrustees, nClients, payloadLength, reportingLimit, trusteesIp, useUDP)

	//start the server waiting for clients
	newClientConnectionsChan        := make(chan net.Conn) 	          //channel with unparsed clients
	go relayServerListener(relayPort, newClientConnectionsChan)

	//start the client parser
	newClientWithIdAndPublicKeyChan := make(chan prifinet.NodeRepresentation)  //channel with parsed clients
	go welcomeNewClients(newClientConnectionsChan, newClientWithIdAndPublicKeyChan, useUDP)

	stateMachineLogger.StateChange("protocol-setup")

	//start the actual protocol
	relayState.connectToAllTrustees()
	relayState.waitForDefaultNumberOfClients(newClientWithIdAndPublicKeyChan)
	relayState.advertisePublicKeys()	
	err := relayState.organizeRoundScheduling()

	var isProtocolRunning = false
	if err != nil {
		prifilog.Println(prifilog.RECOVERABLE_ERROR, "Relay Handler : round scheduling went wrong, restarting the configuration protocol")

		//disconnect all clients
		for i:=0; i<len(relayState.clients); i++{
			relayState.clients[i].Conn.Close()
			relayState.clients[i].Connected = false
		}	
		restartProtocol(relayState, make([]prifinet.NodeRepresentation, 0));
	} else {
		//copy for subtrhead
		relayStateCopy := relayState.deepClone()
		go processMessageLoop(relayStateCopy)
		isProtocolRunning = true
	}

	//control loop
	var endOfProtocolState int
	newClients := make([]prifinet.NodeRepresentation, 0)

	for {

		select {
			case protocolHasFailed := <- protocolFailed:
				prifilog.Println(prifilog.NOTIFICATION, "Relay Handler : Processing loop has failed with status", protocolHasFailed)
				isProtocolRunning = false
				//TODO : re-run setup, something went wrong. Maybe restart from 0 ?

			case deconnectedClient := <- deconnectedClients:
				prifilog.Println(prifilog.WARNING, "Client", deconnectedClient, " has been indicated offline")
				relayState.clients[deconnectedClient].Connected = false

			case timedOutClient := <- timedOutClients:
				prifilog.Println(prifilog.WARNING, "Client", timedOutClient, " has been indicated offline (time out)")
				relayState.clients[timedOutClient].Conn.Close()
				relayState.clients[timedOutClient].Connected = false

			case deconnectedTrustee := <- deconnectedTrustees:
				prifilog.Println(prifilog.RECOVERABLE_ERROR, "Trustee", deconnectedTrustee, " has been indicated offline")

			case newClient := <- newClientWithIdAndPublicKeyChan:
				//we tell processMessageLoop to stop when possible
				newClients = append(newClients, newClient)
				if isProtocolRunning {
					prifilog.Println(prifilog.NOTIFICATION, "Relay Handler : new Client is ready, stopping processing loop")
					indicateEndOfProtocol <- PROTOCOL_STATUS_GONNA_RESYNC
				} else {
					prifilog.Println(prifilog.NOTIFICATION, "Relay Handler : new Client is ready, restarting processing loop")
					isProtocolRunning = restartProtocol(relayState, newClients)
					newClients = make([]prifinet.NodeRepresentation, 0)
					prifilog.Println(prifilog.INFORMATION, "Done...")
				}

			case endOfProtocolState = <- indicateEndOfProtocol:
				prifilog.Println(prifilog.INFORMATION, "Relay Handler : main loop stopped (status",endOfProtocolState,"), resyncing")

				if endOfProtocolState != PROTOCOL_STATUS_RESYNCING {
					panic("something went wrong, should not happen")
				}

				isProtocolRunning = restartProtocol(relayState, newClients)
				newClients = make([]prifinet.NodeRepresentation, 0)
			default: 
				//all clear! keep this thread handler load low, (accept changes every X millisecond)
				time.Sleep(CONTROL_LOOP_SLEEP_TIME)
				//prifilog.StatisticReport("relay", "CONTROL_LOOP_SLEEP_TIME", CONTROL_LOOP_SLEEP_TIME.String())
		}
	}
}

func restartProtocol(relayState *RelayState, newClients []prifinet.NodeRepresentation) bool {
	relayState.excludeDisconnectedClients() 				
	relayState.disconnectFromAllTrustees()

	//add the new clients to the previous (filtered) list
	for i:=0; i<len(newClients); i++{
		relayState.addNewClient(newClients[i])
		prifilog.Println(prifilog.NOTIFICATION, "Adding new client", newClients[i])
	}
	relayState.nClients = len(relayState.clients)

	//if we dont have enough client, stop.
	if len(relayState.clients) == 0{
		prifilog.Println(prifilog.WARNING, "Relay Handler : not enough client, stopping and waiting...")
		return false
	} else {
		//re-advertise the configuration 	
		relayState.connectToAllTrustees()
		relayState.advertisePublicKeys()
		err := relayState.organizeRoundScheduling()
		if err != nil {
			prifilog.Println(prifilog.RECOVERABLE_ERROR, "Relay Handler : round scheduling went wrong, restarting the configuration protocol")

			//disconnect all clients
			for i:=0; i<len(relayState.clients); i++{
				relayState.clients[i].Conn.Close()
				relayState.clients[i].Connected = false
			}	
			return restartProtocol(relayState, make([]prifinet.NodeRepresentation, 0));
		}

		if INBETWEEN_CONFIG_SLEEP_TIME != 0 {
			time.Sleep(INBETWEEN_CONFIG_SLEEP_TIME)
			prifilog.StatisticReport("relay", "INBETWEEN_CONFIG_SLEEP_TIME", INBETWEEN_CONFIG_SLEEP_TIME.String())
		}

		//process message loop
		relayStateCopy := relayState.deepClone()
		go processMessageLoop(relayStateCopy)

		return true
	}
}

func (relayState *RelayState) advertisePublicKeys() error{
	defer prifilog.TimeTrack("relay", "advertisePublicKeys", time.Now())

	//Prepare the messages
	dataForClients, err  := prifinet.MarshalNodeRepresentationArrayToByteArray(relayState.trustees)

	if err != nil {
		return err
	}

	dataForTrustees, err := prifinet.MarshalNodeRepresentationArrayToByteArray(relayState.clients)

	if err != nil {
		return err
	}

	//craft the message for clients
	messageForClients := make([]byte, 6 + len(dataForClients))
	binary.BigEndian.PutUint16(messageForClients[0:2], uint16(prifinet.MESSAGE_TYPE_PUBLICKEYS))
	binary.BigEndian.PutUint32(messageForClients[2:6], uint32(relayState.nClients))
	copy(messageForClients[6:], dataForClients)

	//TODO : would be cleaner if the trustees used the same structure for the message

	//broadcast to the clients
	prifinet.NUnicastMessageToNodes(relayState.clients, messageForClients)
	prifinet.NUnicastMessageToNodes(relayState.trustees, dataForTrustees)
	prifilog.Println(prifilog.NOTIFICATION, "Advertising done, to", len(relayState.clients), "clients and", len(relayState.trustees), "trustees")

	return nil
}

func (relayState *RelayState) organizeRoundScheduling() error {
	defer prifilog.TimeTrack("relay", "organizeRoundScheduling", time.Now())

	ephPublicKeys := make([]abstract.Point, relayState.nClients)

	//collect ephemeral keys
	prifilog.Println(prifilog.INFORMATION, "Waiting for", relayState.nClients, "ephemeral keys")
	for i := 0; i < relayState.nClients; i++ {
		ephPublicKeys[i] = nil
		for ephPublicKeys[i] == nil {

			pkRead := false
			var pk abstract.Point = nil

			for !pkRead {

				buffer, err := prifinet.ReadMessage(relayState.clients[i].Conn)
				publicKey := config.CryptoSuite.Point()
				msgType := int(binary.BigEndian.Uint16(buffer[0:2]))

				if msgType == prifinet.MESSAGE_TYPE_PUBLICKEYS {
					err2 := publicKey.UnmarshalBinary(buffer[2:])

					if err2 != nil {
						prifilog.Println(prifilog.INFORMATION, "Reading client", i, "ephemeral key")
						return err
					}
					pk = publicKey
					break

				} else if msgType != prifinet.MESSAGE_TYPE_PUBLICKEYS {
					//append data in the buffer
					prifilog.Println(prifilog.WARNING, "organizeRoundScheduling: trying to read a public key message, got a data message; discarding, checking for public key in next message...")
					continue
				}
			}

			ephPublicKeys[i] = pk
		}
	}

	prifilog.Println(prifilog.INFORMATION, "Relay: collected all ephemeral public keys")

	// prepare transcript
	G_s             := make([]abstract.Point, relayState.nTrustees)
	ephPublicKeys_s := make([][]abstract.Point, relayState.nTrustees)
	proof_s         := make([][]byte, relayState.nTrustees)

	//ask each trustee in turn to do the oblivious shuffle
	G := config.CryptoSuite.Point().Base()
	for j := 0; j < relayState.nTrustees; j++ {

		prifinet.WriteBaseAndPublicKeyToConn(relayState.trustees[j].Conn, G, ephPublicKeys)
		prifilog.Println(prifilog.INFORMATION, "Trustee", j, "is shuffling...")

		base2, ephPublicKeys2, proof, err := prifinet.ParseBasePublicKeysAndProofFromConn(relayState.trustees[j].Conn)

		if err != nil {
			return err
		}

		prifilog.Println(prifilog.INFORMATION, "Trustee", j, "is done shuffling")

		//collect transcript
		G_s[j]             = base2
		ephPublicKeys_s[j] = ephPublicKeys2
		proof_s[j]         = proof

		//next trustee get this trustee's output
		G            = base2
		ephPublicKeys = ephPublicKeys2
	}

	prifilog.Println(prifilog.INFORMATION, "All trustees have shuffled, sending the transcript...")

	//pack transcript
	transcriptBytes := make([]byte, 0)
	for i:=0; i<len(G_s); i++ {
		G_s_i_bytes, _ := G_s[i].MarshalBinary()
		transcriptBytes = append(transcriptBytes, prifinet.IntToBA(len(G_s_i_bytes))...)
		transcriptBytes = append(transcriptBytes, G_s_i_bytes...)

		//prifilog.Println("G_S_", i)
		//prifilog.Println(hex.Dump(G_s_i_bytes))
	}
	for i:=0; i<len(ephPublicKeys_s); i++ {

		pkArray := make([]byte, 0)
		for k:=0; k<len(ephPublicKeys_s[i]); k++{
			pkBytes, _ := ephPublicKeys_s[i][k].MarshalBinary()
			pkArray = append(pkArray, prifinet.IntToBA(len(pkBytes))...)
			pkArray = append(pkArray, pkBytes...)
			//prifilog.Println("Packing key", k)
		}

		transcriptBytes = append(transcriptBytes, prifinet.IntToBA(len(pkArray))...)
		transcriptBytes = append(transcriptBytes, pkArray...)

		//prifilog.Println("pkArray_", i)
		//prifilog.Println(hex.Dump(pkArray))
	}
	for i:=0; i<len(proof_s); i++ {
		transcriptBytes = append(transcriptBytes, prifinet.IntToBA(len(proof_s[i]))...)
		transcriptBytes = append(transcriptBytes, proof_s[i]...)

		//prifilog.Println("G_S_", i)
		//prifilog.Println(hex.Dump(proof_s[i]))
	}

	//broadcast to trustees
	prifinet.NUnicastMessageToNodes(relayState.trustees, transcriptBytes)

	//wait for the signature for each trustee
	signatures := make([][]byte, relayState.nTrustees)
	for j := 0; j < relayState.nTrustees; j++ {
 
 		buffer, err := prifinet.ReadMessage(relayState.trustees[j].Conn)
		if err != nil {
			prifilog.Println(prifilog.RECOVERABLE_ERROR, "Relay, couldn't read signature from trustee " + err.Error())
			return err
		}

		sigSize := int(binary.BigEndian.Uint32(buffer[0:4]))
		sig := make([]byte, sigSize)
		copy(sig[:], buffer[4:4+sigSize])
		
		signatures[j] = sig

		prifilog.Println(prifilog.INFORMATION, "Collected signature from trustee", j)
	}

	prifilog.Println(prifilog.INFORMATION, "Crafting signature message for clients...")

	sigMsg := make([]byte, 0)

	//the final shuffle is the one from the latest trustee
	lastPermutation := relayState.nTrustees - 1
	G_s_i_bytes, err := G_s[lastPermutation].MarshalBinary()
	if err != nil {
		return err
	}

	//pack the final base
	sigMsg = append(sigMsg, prifinet.IntToBA(len(G_s_i_bytes))...)
	sigMsg = append(sigMsg, G_s_i_bytes...)

	//pack the ephemeral shuffle
	pkArray, err := prifinet.MarshalPublicKeyArrayToByteArray(ephPublicKeys_s[lastPermutation])

	if err != nil {
		return err
	}

	sigMsg = append(sigMsg, prifinet.IntToBA(len(pkArray))...)
	sigMsg = append(sigMsg, pkArray...)

	//pack the trustee's signatures
	packedSignatures := make([]byte, 0)
	for j := 0; j < relayState.nTrustees; j++ {
		packedSignatures = append(packedSignatures, prifinet.IntToBA(len(signatures[j]))...)
		packedSignatures = append(packedSignatures, signatures[j]...)
	}
	sigMsg = append(sigMsg, prifinet.IntToBA(len(packedSignatures))...)
	sigMsg = append(sigMsg, packedSignatures...)

	//send to clients
	prifinet.NUnicastMessageToNodes(relayState.clients, sigMsg)

	prifilog.Println(prifilog.INFORMATION, "Oblivious shuffle & signatures sent !")
	return nil

	/* 
	//obsolete, of course in practice the client do the verification (relay is untrusted)
	prifilog.Println("We verify on behalf of client")

	M := make([]byte, 0)
	M = append(M, G_s_i_bytes...)
	for k:=0; k<len(ephPublicKeys_s[lastPermutation]); k++{
		prifilog.Println("Embedding eph key")
		prifilog.Println(ephPublicKeys_s[lastPermutation][k])
		pkBytes, _ := ephPublicKeys_s[lastPermutation][k].MarshalBinary()
		M = append(M, pkBytes...)
	}

	prifilog.Println("The message we're gonna verify is :")
	prifilog.Println(hex.Dump(M))

	for j := 0; j < relayState.nTrustees; j++ {
		sigMsg = append(sigMsg, prifinet.IntToBA(len(signatures[j]))...)
		sigMsg = append(sigMsg, signatures[j]...)

		prifilog.Println("Verifying for trustee", j)
		err := crypto.SchnorrVerify(config.CryptoSuite, M, relayState.trustees[j].PublicKey, signatures[j])

		prifilog.Println("Signature was :")
		prifilog.Println(hex.Dump(signatures[j]))

		if err == nil {
			prifilog.Println("Signature OK !")
		} else {
			panic(err.Error())
		}
	}
	*/
}


func processMessageLoop(relayState *RelayState){
	//TODO : if something fail, send true->protocolFailed

	stateMachineLogger.StateChange("protocol-mainloop")

	/*
	prifilog.Println(prifilog.NOTIFICATION, "")
	prifilog.Println(prifilog.NOTIFICATION, "#################################")
	prifilog.Println(prifilog.NOTIFICATION, "# Configuration updated, running")
	prifilog.Println(prifilog.NOTIFICATION, "#", relayState.nClients, "clients", relayState.nTrustees, "trustees")

	for i := 0; i<len(relayState.clients); i++ {
		prifilog.Println(prifilog.NOTIFICATION, "# Client", relayState.clients[i].Id, " on port ", relayState.clients[i].Conn.LocalAddr())
	}
	for i := 0; i<len(relayState.trustees); i++ {
		prifilog.Println(prifilog.NOTIFICATION, "# Trustee", relayState.trustees[i].Id, " on port ", relayState.trustees[i].Conn.LocalAddr())
	}
	prifilog.Println(prifilog.NOTIFICATION, "#################################")
	prifilog.Println(prifilog.NOTIFICATION, "")
	*/


	prifilog.InfoReport(prifilog.NOTIFICATION, "relay", fmt.Sprintf("new setup, %v clients and %v trustees", relayState.nClients, relayState.nTrustees))

	for i := 0; i<len(relayState.clients); i++ {
		prifilog.InfoReport(prifilog.NOTIFICATION, "relay", fmt.Sprintf("new setup, client %v on %v", relayState.clients[i].Id, relayState.clients[i].Conn.LocalAddr()))
	}
	for i := 0; i<len(relayState.trustees); i++ {
		prifilog.InfoReport(prifilog.NOTIFICATION, "relay", fmt.Sprintf("new setup, trustee %v on %v", relayState.trustees[i].Id, relayState.trustees[i].Conn.LocalAddr()))
	}

	stats := prifilog.EmptyStatistics(relayState.ReportingLimit)

	// Create ciphertext slice bufferfers for all clients and trustees
	clientPayloadLength := relayState.CellCoder.ClientCellSize(relayState.PayloadLength)
	clientsPayloadData  := make([][]byte, relayState.nClients)
	for i := 0; i < relayState.nClients; i++ {
		clientsPayloadData[i] = make([]byte, clientPayloadLength)
	}

	trusteePayloadLength := relayState.CellCoder.TrusteeCellSize(relayState.PayloadLength)
	trusteesPayloadData  := make([][]byte, relayState.nTrustees)
	for i := 0; i < relayState.nTrustees; i++ {
		trusteesPayloadData[i] = make([]byte, trusteePayloadLength)
	}

	socksProxyConnections := make(map[int]chan<- []byte)
	downstream            := make(chan prifinet.DataWithConnectionId)
	priorityDownStream    := make([]prifinet.DataWithConnectionId, 0)
	nulldown              := prifinet.DataWithConnectionId{} // default empty downstream cell
	window                := 2           // Maximum cells in-flight
	inflight              := 0         // Current cells in-flight

	currentSetupContinues := true
	
	for currentSetupContinues {

		//prifilog.Println(".")

		//if needed, we bound the number of round per second
		if INBETWEEN_ROUND_SLEEP_TIME != 0 {
			time.Sleep(INBETWEEN_ROUND_SLEEP_TIME)
			prifilog.StatisticReport("relay", "INBETWEEN_ROUND_SLEEP_TIME", INBETWEEN_ROUND_SLEEP_TIME.String())
		}

		//if the main thread tells us to stop (for re-setup)
		tellClientsToResync := false
		var mainThreadStatus int
		select {
			case mainThreadStatus = <- indicateEndOfProtocol:
				if mainThreadStatus == PROTOCOL_STATUS_GONNA_RESYNC {
					prifilog.Println(prifilog.NOTIFICATION, "Main thread status is PROTOCOL_STATUS_GONNA_RESYNC, gonna warn the clients")
					tellClientsToResync = true
				}
			default:
		}

		//we report the speed, bytes exchanged, etc
		stats.Report()
		if stats.ReportingDone() {
			prifilog.Println(prifilog.WARNING, "Reporting limit matched; exiting the relay")
			break;
		}

		// See if there's any downstream data to forward.
		var downbuffer prifinet.DataWithConnectionId 
		if len(priorityDownStream) > 0 {
			downbuffer         = priorityDownStream[0]

			if len(priorityDownStream) == 1 {
				priorityDownStream = nil
			} else {
				priorityDownStream = priorityDownStream[1:]
			}
		} else {
			select {
				case downbuffer = <-downstream: // some data to forward downstream
				default: 
					downbuffer = nulldown
			}
		}

		//compute the message type; if MESSAGE_TYPE_DATA_AND_RESYNC, the clients know they will resync
		msgType := prifinet.MESSAGE_TYPE_DATA
		if tellClientsToResync{
			msgType = prifinet.MESSAGE_TYPE_DATA_AND_RESYNC
			currentSetupContinues = false
		}

		//craft the message for clients
		downstreamDataPayloadLength := len(downbuffer.Data)
		downstreamData := make([]byte, 6+downstreamDataPayloadLength)
		binary.BigEndian.PutUint16(downstreamData[0:2], uint16(msgType))
		binary.BigEndian.PutUint32(downstreamData[2:6], uint32(downbuffer.ConnectionId)) //this is the SOCKS connection ID
		copy(downstreamData[6:], downbuffer.Data)

		// Broadcast the downstream data to all clients.
		prifinet.NUnicastMessageToNodes(relayState.clients, downstreamData)
		stats.AddDownstreamCell(int64(downstreamDataPayloadLength))

		inflight++
		if inflight < window {
			continue // Get more cells in flight
		}

		relayState.CellCoder.DecodeStart(relayState.PayloadLength, relayState.MessageHistory)

		// Collect a cell ciphertext from each trustee
		errorInThisCell := false
		for i := 0; i < relayState.nTrustees; i++ {	

			if errorInThisCell {
				break
			}

			//TODO : add a channel for timeout trustee
			data, err := prifinet.ReadWithTimeOut(i, relayState.trustees[i].Conn, trusteePayloadLength, CLIENT_READ_TIMEOUT, deconnectedTrustees, deconnectedTrustees)

			if err {
				errorInThisCell = true
			}

			relayState.CellCoder.DecodeTrustee(data)
		}

		// Collect an upstream ciphertext from each client
		for i := 0; i < relayState.nClients; i++ {

			if errorInThisCell {
				break
			}

			data, err := prifinet.ReadWithTimeOut(i, relayState.clients[i].Conn, clientPayloadLength, CLIENT_READ_TIMEOUT, timedOutClients, deconnectedClients)

			if err {
				errorInThisCell = true
			}

			relayState.CellCoder.DecodeClient(data)
		}

		if errorInThisCell {
			
			prifilog.Println(prifilog.WARNING, "Relay main loop : Cell will be invalid, some party disconnected. Warning the clients...")

			//craft the message for clients
			downstreamData := make([]byte, 10)
			binary.BigEndian.PutUint16(downstreamData[0:2], uint16(prifinet.MESSAGE_TYPE_LAST_UPLOAD_FAILED))
			binary.BigEndian.PutUint32(downstreamData[2:6], uint32(downbuffer.ConnectionId)) //this is the SOCKS connection ID
			prifinet.NUnicastMessageToNodes(relayState.clients, downstreamData)

			break
		} else {
			upstreamPlaintext := relayState.CellCoder.DecodeCell()
			inflight--

			stats.AddUpstreamCell(int64(relayState.PayloadLength))

			// Process the decoded cell

			//check if we have a latency test message
			pattern     := int(binary.BigEndian.Uint16(upstreamPlaintext[0:2]))
			if pattern == 43690 { //1010101010101010
				//clientId  := uint16(binary.BigEndian.Uint16(upstreamPlaintext[2:4]))
				//timestamp := uint64(binary.BigEndian.Uint64(upstreamPlaintext[4:12]))

				cellDown := prifinet.DataWithConnectionId{-1, upstreamPlaintext}
				priorityDownStream = append(priorityDownStream, cellDown)

				continue //the rest is for SOCKS
			}

			if upstreamPlaintext == nil {
				continue // empty or corrupt upstream cell
			}
			if len(upstreamPlaintext) != relayState.PayloadLength {
				panic("DecodeCell produced wrong-size payload")
			}

			// Decode the upstream cell header (may be empty, all zeros)
			socksConnId     := int(binary.BigEndian.Uint32(upstreamPlaintext[0:4]))
			socksDataLength := int(binary.BigEndian.Uint16(upstreamPlaintext[4:6]))

			if socksConnId == prifinet.SOCKS_CONNECTION_ID_EMPTY {
				continue 
			}

			socksConn := socksProxyConnections[socksConnId]

			// client initiating new connection
			if socksConn == nil { 
				socksConn = newSOCKSProxyHandler(socksConnId, downstream)
				socksProxyConnections[socksConnId] = socksConn
			}

			if 6+socksDataLength > relayState.PayloadLength {
				log.Printf("upstream cell invalid length %d", 6+socksDataLength)
				continue
			}

			socksConn <- upstreamPlaintext[6 : 6+socksDataLength]
		}
	}

	if INBETWEEN_CONFIG_SLEEP_TIME != 0 {
		time.Sleep(INBETWEEN_CONFIG_SLEEP_TIME)
		prifilog.StatisticReport("relay", "INBETWEEN_CONFIG_SLEEP_TIME", INBETWEEN_CONFIG_SLEEP_TIME.String())
	}

	indicateEndOfProtocol <- PROTOCOL_STATUS_RESYNCING

	stateMachineLogger.StateChange("protocol-resync")
}

func newSOCKSProxyHandler(connId int, downstreamData chan<- prifinet.DataWithConnectionId) chan<- []byte {
	upstreamData := make(chan []byte)
	//go prifinet.RelaySocksProxy(connId, upstreamData, downstreamData)
	return upstreamData
}

func connectToTrustee(trusteeId int, trusteeHostAddr string, relayState *RelayState) error {
	prifilog.Println(prifilog.NOTIFICATION, "Relay connecting to trustee", trusteeId, "on address", trusteeHostAddr)

	var conn net.Conn = nil
	var err error = nil

	//connect
	for conn == nil{
		conn, err = net.Dial("tcp", trusteeHostAddr)
		if err != nil {
			prifilog.Println(prifilog.RECOVERABLE_ERROR, "Can't connect to trustee on "+trusteeHostAddr+"; "+err.Error())
			conn = nil
			time.Sleep(FAILED_CONNECTION_WAIT_BEFORE_RETRY)
		}
	}

	//tell the trustee server our parameters
	buffer := make([]byte, 16)
	binary.BigEndian.PutUint32(buffer[0:4], uint32(relayState.PayloadLength))
	binary.BigEndian.PutUint32(buffer[4:8], uint32(relayState.nClients))
	binary.BigEndian.PutUint32(buffer[8:12], uint32(relayState.nTrustees))
	binary.BigEndian.PutUint32(buffer[12:16], uint32(trusteeId))

	prifilog.Println(prifilog.NOTIFICATION, "Writing; setup is", relayState.nClients, relayState.nTrustees, "role is", trusteeId, "cellSize ", relayState.PayloadLength)

	err2 := prifinet.WriteMessage(conn, buffer)

	if err2 != nil {
		prifilog.Println(prifilog.RECOVERABLE_ERROR, "Error writing to socket:" + err2.Error())
		return err2
	}
	
	// Read the incoming connection into the buffer.
	buffer2, err2 := prifinet.ReadMessage(conn)
	if err2 != nil {
	    prifilog.Println(prifilog.RECOVERABLE_ERROR, "error reading:", err.Error())
	    return err2
	}

	publicKey := config.CryptoSuite.Point()
	err3 := publicKey.UnmarshalBinary(buffer2)

	if err3 != nil {
		prifilog.Println(prifilog.RECOVERABLE_ERROR, "can't unmarshal trustee key ! " + err3.Error())
		return err3
	}

	prifilog.Println(prifilog.INFORMATION, "Trustee", trusteeId, "is connected.")
	
	newTrustee := prifinet.NodeRepresentation{trusteeId, conn, true, publicKey}

	//side effects
	relayState.trustees = append(relayState.trustees, newTrustee)

	return nil
}

func relayServerListener(listeningPort string, newConnection chan net.Conn) {
	listeningSocket, err := net.Listen("tcp", listeningPort)
	if err != nil {
		panic("Can't open listen socket:" + err.Error())
	}

	for {
		conn, err2 := listeningSocket.Accept()
		if err != nil {
			prifilog.Println(prifilog.RECOVERABLE_ERROR, "Relay : can't accept client. ", err2.Error())
		}
		newConnection <- conn
	}
}

func relayParseClientParamsAux(tcpConn net.Conn, clientsUseUDP bool) prifinet.NodeRepresentation {

	message, err := prifinet.ReadMessage(tcpConn)
	if err != nil {
		panic("Read error:" + err.Error())
	}

	//check that the node ID is not used
	nextFreeId := 0
	for i:=0; i<len(relayState.clients); i++{

		if relayState.clients[i].Id == nextFreeId {
			nextFreeId++
		}
	}
	prifilog.Println(prifilog.NOTIFICATION, "Client connected, assigning his ID to", nextFreeId)
	nodeId := nextFreeId

	publicKey := config.CryptoSuite.Point()
	err2 := publicKey.UnmarshalBinary(message)

	if err2 != nil {
		prifilog.Println(prifilog.SEVERE_ERROR, "can't unmarshal client key ! " + err2.Error())
		panic("can't unmarshal client key ! " + err2.Error())
	}

	newClient := prifinet.NodeRepresentation{nodeId, tcpConn, true, publicKey}

	//client ready, say hello over UDP
	if clientsUseUDP {

		time.Sleep(time.Second * 5)

		ServerAddr,_ := net.ResolveUDPAddr("udp",tcpConn.RemoteAddr().String())	 
	    LocalAddr, _ := net.ResolveUDPAddr("udp", tcpConn.LocalAddr().String())

		prifilog.Println(prifilog.INFORMATION, " writing hello Message to udp")
		m := []byte("You're client "+strconv.Itoa(nodeId))

		prifilog.Println(prifilog.SEVERE_ERROR, "Connecting on UDP to  " + ServerAddr.String())
		udpConn, err3 := net.DialUDP("udp", LocalAddr, ServerAddr)

		if err3 != nil {
			prifilog.Println(prifilog.SEVERE_ERROR, "can't udp conn ! " + err3.Error())
			panic("panic")
		}

		buffer := make([]byte, len(m)+6)
		binary.BigEndian.PutUint16(buffer[0:2], uint16(config.LLD_PROTOCOL_VERSION))
		binary.BigEndian.PutUint32(buffer[2:6], uint32(len(m)))
		copy(buffer[6:], m)

		udpConn.WriteToUDP(buffer, ServerAddr)

		prifilog.Println(prifilog.INFORMATION, "Message written")
		time.Sleep(time.Second * 100)

		//prifinet.WriteMessage(udpConn, m)
	}

	return newClient
}

func relayParseClientParams(tcpConn net.Conn, newClientChan chan prifinet.NodeRepresentation, clientsUseUDP bool) {

	newClient := relayParseClientParamsAux(tcpConn, clientsUseUDP)
	newClientChan <- newClient
}