package net

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"io"
	"time"
	"errors"
	"net"
	"github.com/lbarman/crypto/abstract"
	"github.com/lbarman/prifi/config"
)

// return data, error
func ReadWithTimeOut(nodeId int, conn net.Conn, length int, timeout time.Duration, chanForTimeoutNode chan int, chanForDisconnectedNode chan int) ([]byte, bool) {
	
	//read with timeout
	timeoutChan := make(chan bool, 1)
	errorChan   := make(chan bool, 1)
	dataChan    := make(chan []byte)
	
	go func() {
	    time.Sleep(timeout)
	    timeoutChan <- true
	}()
	
	go func() {
		dataHolder := make([]byte, length)
		n, err := io.ReadFull(conn, dataHolder)

		if err != nil || n < length {
			errorChan <- true
		} else {
	    	dataChan <- dataHolder
		}
	}()

	var data []byte
	errorDuringRead := false
	select
	{
		case data = <- dataChan:

		case <-timeoutChan:
			errorDuringRead = true
			chanForTimeoutNode <- nodeId

		case <-errorChan:
			errorDuringRead = true
			chanForDisconnectedNode <- nodeId
	}

	return data, errorDuringRead
}

func ParseTranscript(conn net.Conn, nClients int, nTrustees int) ([]abstract.Point, [][]abstract.Point, [][]byte, error) {
	buffer := make([]byte, 4096)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("couldn't read transcript from relay " + err.Error())
		return nil, nil, nil, err
	}
	
	G_s             := make([]abstract.Point, nTrustees)
	ephPublicKeys_s := make([][]abstract.Point, nTrustees)
	proof_s         := make([][]byte, nTrustees)

	//read the G_s
	currentByte := 0
	i := 0
	for {
		if currentByte+4 > len(buffer) {
			break; //we reached the end of the array
		}

		length := int(binary.BigEndian.Uint32(buffer[currentByte:currentByte+4]))

		if length == 0 {
			break; //we reached the end of the array
		}

		G_S_i_Bytes := buffer[currentByte+4:currentByte+4+length]

		fmt.Println("G_S_", i)
		fmt.Println(hex.Dump(G_S_i_Bytes))

		base := config.CryptoSuite.Point()
		err2 := base.UnmarshalBinary(G_S_i_Bytes)
		if err2 != nil {
			fmt.Println(">>>>can't unmarshal base n°"+strconv.Itoa(i)+" ! " + err2.Error())
			return nil, nil, nil, err
		}

		G_s[i] = base
		fmt.Println("Read G_S[", i, "]")

		currentByte += 4 + length
		i += 1

		if i == nTrustees {
			break
		}
	}

	//read the ephemeral public keys
	i = 0
	for {
		if currentByte+4 > len(buffer) {
			break; //we reached the end of the array
		}

		length := int(binary.BigEndian.Uint32(buffer[currentByte:currentByte+4]))

		if length == 0 {
			break; //we reached the end of the array
		}

		ephPublicKeysBytes := buffer[currentByte+4:currentByte+4+length]

		ephPublicKeys := make([]abstract.Point, 0)

		fmt.Println("Ephemeral_PKS_", i)
		fmt.Println(hex.Dump(ephPublicKeysBytes))

		currentByte2 := 0
		j := 0
		for {
			if currentByte2+4 > len(ephPublicKeysBytes) {
				break; //we reached the end of the array
			}

			length := int(binary.BigEndian.Uint32(ephPublicKeysBytes[currentByte2:currentByte2+4]))

			if length == 0 {
				break; //we reached the end of the array
			}

			ephPublicKeyIJBytes := ephPublicKeysBytes[currentByte2+4:currentByte2+4+length]
			ephPublicKey := config.CryptoSuite.Point()
			err2 := ephPublicKey.UnmarshalBinary(ephPublicKeyIJBytes)
			if err2 != nil {
				fmt.Println(">>>>can't unmarshal public key n°"+strconv.Itoa(i)+","+strconv.Itoa(j)+" ! " + err2.Error())
				return nil, nil, nil, err
			}
			
			ephPublicKeys = append(ephPublicKeys, ephPublicKey)
			fmt.Println("Read EphemeralPublicKey[", i, "][", j, "]")

			currentByte2 += 4 + length
			j += 1

			if j == nClients{
				break
			}
		}

		fmt.Println("Read EphemeralPublicKey[", i, "]")
		ephPublicKeys_s[i] = ephPublicKeys

		currentByte += 4 + length
		i += 1

		if i == nTrustees {
			break
		}
	}

	//read the Proofs
	i = 0
	for {
		if currentByte+4 > len(buffer) {
			break; //we reached the end of the array
		}

		length := int(binary.BigEndian.Uint32(buffer[currentByte:currentByte+4]))

		if length == 0 {
			break; //we reached the end of the array
		}

		proofBytes := buffer[currentByte+4:currentByte+4+length]
		fmt.Println("Read Proof[", i, "]")

		proof_s[i] = proofBytes

		currentByte += 4 + length
		i += 1

		if i == nTrustees {
			break
		}
	}

	return G_s, ephPublicKeys_s, proof_s, nil
}

func ParsePublicKeyFromConn(conn net.Conn) (abstract.Point, error) {
	buffer := make([]byte, 512)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("ParsePublicKeyFromConn : Read error:" + err.Error())
		return nil, err
	}

	version := int(binary.BigEndian.Uint32(buffer[0:4]))
	if version != config.LLD_PROTOCOL_VERSION {
		fmt.Println("ParsePublicKeyFromConn caught a data message")
		return nil, nil
	}

	keySize := int(binary.BigEndian.Uint32(buffer[8:12]))
	keyBytes := buffer[12:(12+keySize)] 

	publicKey := config.CryptoSuite.Point()
	err2 := publicKey.UnmarshalBinary(keyBytes)

	if err2 != nil {
		fmt.Println("ParsePublicKeyFromConn : can't unmarshal ephemeral client key ! " + err2.Error())
		return nil, err
	}

	return publicKey, nil
}

func ParseBaseAndPublicKeysFromConn(conn net.Conn) (abstract.Point, []abstract.Point, error) {
	buffer := make([]byte, 1024)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("ParseBaseAndPublicKeysFromConn, couldn't read. " + err.Error())
		return nil, nil, err
	}

	baseSize := int(binary.BigEndian.Uint32(buffer[0:4]))
	keysSize := int(binary.BigEndian.Uint32(buffer[4+baseSize:8+baseSize]))

	baseBytes := buffer[4:4+baseSize] 
	keysBytes := buffer[8+baseSize:8+baseSize+keysSize] 

	base := config.CryptoSuite.Point()
	err2 := base.UnmarshalBinary(baseBytes)
	if err2 != nil {
		fmt.Println("ParseBaseAndPublicKeysFromConn : can't unmarshal client key ! " + err2.Error())
		return nil, nil, err2
	}

	publicKeys, err := UnMarshalPublicKeyArrayFromByteArray(keysBytes, config.CryptoSuite)
	if err != nil {
		return nil, nil, err
	}
	return base, publicKeys, nil
}

func IntToBA(x int) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf[0:4], uint32(x))
	return buf
}


func ParseBasePublicKeysAndProofFromConn(conn net.Conn) (abstract.Point, []abstract.Point, []byte, error) {
	buffer := make([]byte, 1024)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("ParseBaseAndPublicKeysFromConn, couldn't read. " + err.Error())
		return nil, nil, nil, err
	}
		
	baseSize := int(binary.BigEndian.Uint32(buffer[0:4]))
	keysSize := int(binary.BigEndian.Uint32(buffer[4+baseSize:8+baseSize]))
	proofSize := int(binary.BigEndian.Uint32(buffer[8+baseSize+keysSize:12+baseSize+keysSize]))

	baseBytes := buffer[4:4+baseSize] 
	keysBytes := buffer[8+baseSize:8+baseSize+keysSize] 
	proof := buffer[12+baseSize+keysSize:12+baseSize+keysSize+proofSize] 

	base := config.CryptoSuite.Point()
	err2 := base.UnmarshalBinary(baseBytes)
	if err2 != nil {
		fmt.Println("ParseBasePublicKeysAndProofFromConn : can't unmarshal client key ! " + err2.Error())
		return nil, nil, nil, err2
	}

	publicKeys, err := UnMarshalPublicKeyArrayFromByteArray(keysBytes, config.CryptoSuite)
	if err != nil {
		return nil, nil, nil, err
	}
	return base, publicKeys, proof, nil
}


func ParseBasePublicKeysAndTrusteeSignaturesFromConn(conn net.Conn) (abstract.Point, []abstract.Point, [][]byte, error) {
	buffer := make([]byte, 4096)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("ParseBasePublicKeysAndTrusteeProofFromConn, couldn't read. " + err.Error())
		return nil, nil, nil, err
	}
		
	baseSize := int(binary.BigEndian.Uint32(buffer[0:4]))
	keysSize := int(binary.BigEndian.Uint32(buffer[4+baseSize:8+baseSize]))
	signaturesSize := int(binary.BigEndian.Uint32(buffer[8+baseSize+keysSize:12+baseSize+keysSize]))

	fmt.Println("Signature size", signaturesSize)

	baseBytes := buffer[4:4+baseSize] 
	keysBytes := buffer[8+baseSize:8+baseSize+keysSize] 
	signaturesBytes := buffer[12+baseSize+keysSize:12+baseSize+keysSize+signaturesSize] 

	base := config.CryptoSuite.Point()
	err2 := base.UnmarshalBinary(baseBytes)
	if err2 != nil {
		fmt.Println("ParseBasePublicKeysAndProofFromConn : can't unmarshal client key ! " + err2.Error())
		return nil, nil, nil, err2
	}

	publicKeys, err := UnMarshalPublicKeyArrayFromByteArray(keysBytes, config.CryptoSuite)

	if err != nil {
		return nil, nil, nil, err
	}

	//now read the proofs
	//read the G_s
	currentByte := 0
	signatures := make([][]byte, 0)
	i := 0
	for {
		if currentByte+4 > len(buffer) {
			break; //we reached the end of the array
		}

		length := int(binary.BigEndian.Uint32(signaturesBytes[currentByte:currentByte+4]))

		if length == 0 {
			break; //we reached the end of the array
		}

		thisSig := signaturesBytes[currentByte+4:currentByte+4+length]

		fmt.Println("thisSig_", i)
		fmt.Println(hex.Dump(thisSig))

		signatures = append(signatures, thisSig)

		currentByte += 4 + length
		i += 1
	}

	return base, publicKeys, signatures, nil
}

func WriteBaseAndPublicKeyToConn(conn net.Conn, base abstract.Point, keys []abstract.Point) error {

	baseBytes, err := base.MarshalBinary()
	if err != nil {
		fmt.Println("Marshall error:" + err.Error())
		return err
	}

	publicKEysBytes, err := MarshalPublicKeyArrayToByteArray(keys)

	if err != nil {
		return err
	}

	message := make([]byte, 8+len(baseBytes)+len(publicKEysBytes))

	binary.BigEndian.PutUint32(message[0:4], uint32(len(baseBytes)))
	copy(message[4:4+len(baseBytes)], baseBytes)
	binary.BigEndian.PutUint32(message[4+len(baseBytes):8+len(baseBytes)], uint32(len(publicKEysBytes)))
	copy(message[8+len(baseBytes):], publicKEysBytes)

	_, err2 := conn.Write(message)
	if err2 != nil {
		fmt.Println("Write error:" + err.Error())
		return err2
	}

	return nil
}

func WriteBasePublicKeysAndProofToConn(conn net.Conn, base abstract.Point, keys []abstract.Point, proof []byte) error {
	baseBytes, err := base.MarshalBinary()
	keysBytes, err := MarshalPublicKeyArrayToByteArray(keys)
	if err != nil {
		fmt.Println("Marshall error:" + err.Error())
		return err
	}

	//compose the message
	totalMessageLength := 12+len(baseBytes)+len(keysBytes)+len(proof)
	message := make([]byte, totalMessageLength)

	binary.BigEndian.PutUint32(message[0:4], uint32(len(baseBytes)))
	binary.BigEndian.PutUint32(message[4+len(baseBytes):8+len(baseBytes)], uint32(len(keysBytes)))
	binary.BigEndian.PutUint32(message[8+len(baseBytes)+len(keysBytes):12+len(baseBytes)+len(keysBytes)], uint32(len(proof)))

	copy(message[4:4+len(baseBytes)], baseBytes)
	copy(message[8+len(baseBytes):8+len(baseBytes)+len(keysBytes)], keysBytes)
	copy(message[12+len(baseBytes)+len(keysBytes):12+len(baseBytes)+len(keysBytes)+len(proof)], proof)

	n, err2 := conn.Write(message)
	if err2 != nil {
		fmt.Println("Write error:" + err2.Error())
		return err2
	}
	if n != totalMessageLength {
		fmt.Println("WriteBasePublicKeysAndProofToConn: wrote "+strconv.Itoa(n)+", but expecetd length"+strconv.Itoa(totalMessageLength)+"." + err.Error())
		return errors.New("Could not write to conn")
	}

	return nil
}

func MarshalNodeRepresentationArrayToByteArray(nodes []NodeRepresentation) ([]byte, error) {
	var byteArray []byte

	msgType := make([]byte, 4)
	binary.BigEndian.PutUint32(msgType, uint32(MESSAGE_TYPE_PUBLICKEYS))
	byteArray = append(byteArray, msgType...)

	for i:=0; i<len(nodes); i++ {
		publicKeysBytes, err := nodes[i].PublicKey.MarshalBinary()
		publicKeyLength := make([]byte, 4)
		binary.BigEndian.PutUint32(publicKeyLength, uint32(len(publicKeysBytes)))

		byteArray = append(byteArray, publicKeyLength...)
		byteArray = append(byteArray, publicKeysBytes...)

		if err != nil{
			fmt.Println("can't marshal client public key n°"+strconv.Itoa(i))
			return nil, errors.New("Can't unmarshall")
		}
	}

	return byteArray, nil
}

func BroadcastMessageToNodes(nodes []NodeRepresentation, message []byte) {
	//fmt.Println(hex.Dump(message))

	for i:=0; i<len(nodes); i++ {
		if  nodes[i].Connected {
			n, err := nodes[i].Conn.Write(message)

			//fmt.Println("[", nodes[i].Conn.LocalAddr(), " - ", nodes[i].Conn.RemoteAddr(), "]")

			if n < len(message) || err != nil {
				fmt.Println("Could not broadcast to conn", i, "gonna set it to disconnected.")
				nodes[i].Connected = false
			}
		}
	}
}

func BroadcastMessage(conns []net.Conn, message []byte) error {
	for i:=0; i<len(conns); i++ {
		n, err := conns[i].Write(message)

		fmt.Println("[", conns[i].LocalAddr(), " - ", conns[i].RemoteAddr(), "]")

		if n < len(message) || err != nil {
			fmt.Println("Could not broadcast to conn", i)
			return err
		}
	}
	return nil
}

func TellPublicKey(conn net.Conn, LLD_PROTOCOL_VERSION int, publicKey abstract.Point) error {
	publicKeyBytes, _ := publicKey.MarshalBinary()
	keySize := len(publicKeyBytes)

	//tell the relay our public key (assume user verify through second channel)
	buffer := make([]byte, 8+keySize)
	copy(buffer[8:], publicKeyBytes)
	binary.BigEndian.PutUint32(buffer[0:4], uint32(LLD_PROTOCOL_VERSION))
	binary.BigEndian.PutUint32(buffer[4:8], uint32(keySize))

	n, err := conn.Write(buffer)

	if n < len(buffer) || err != nil {
		fmt.Println("Error writing to socket:" + err.Error())
		return err
	}

	return nil
}

func MarshalPublicKeyArrayToByteArray(publicKeys []abstract.Point) ([]byte, error) {
	var byteArray []byte

	msgType := make([]byte, 4)
	binary.BigEndian.PutUint32(msgType, uint32(MESSAGE_TYPE_PUBLICKEYS))
	byteArray = append(byteArray, msgType...)

	for i:=0; i<len(publicKeys); i++ {
		publicKeysBytes, err := publicKeys[i].MarshalBinary()
		publicKeyLength := make([]byte, 4)
		binary.BigEndian.PutUint32(publicKeyLength, uint32(len(publicKeysBytes)))

		byteArray = append(byteArray, publicKeyLength...)
		byteArray = append(byteArray, publicKeysBytes...)

		//fmt.Println(hex.Dump(publicKeysBytes))
		if err != nil{
			fmt.Println("can't marshal client public key n°"+strconv.Itoa(i))
			return nil, err
		}
	}

	return byteArray, nil
}

func UnMarshalPublicKeyArrayFromConnection(conn net.Conn, cryptoSuite abstract.Suite) ([]abstract.Point, error) {
	//collect the public keys from the trustees
	buffer := make([]byte, 1024)
	_, err := conn.Read(buffer)
	if err != nil {
		fmt.Println("Read error:" + err.Error())
		return nil, err
	}

	pks, err := UnMarshalPublicKeyArrayFromByteArray(buffer, cryptoSuite)
	if err != nil {
		return nil, err
	}
	return pks, nil
}


func UnMarshalPublicKeyArrayFromByteArray(buffer []byte, cryptoSuite abstract.Suite) ([]abstract.Point, error) {

	//will hold the public keys
	var publicKeys []abstract.Point

	//safety check
	messageType := int(binary.BigEndian.Uint32(buffer[0:4]))
	if messageType != 2 {
		fmt.Println("Trying to unmarshall an array, but does not start by 2")
		return nil, errors.New("Wrong message type")
	}

	//parse message
	currentByte := 4
	currentPkId := 0
	for {
		if currentByte+4 > len(buffer) {
			break; //we reached the end of the array
		}

		keyLength := int(binary.BigEndian.Uint32(buffer[currentByte:currentByte+4]))

		if keyLength == 0 {
			break; //we reached the end of the array
		}

		keyBytes := buffer[currentByte+4:currentByte+4+keyLength]

		publicKey := cryptoSuite.Point()
		err2 := publicKey.UnmarshalBinary(keyBytes)
		if err2 != nil {
			fmt.Println(">>>>can't unmarshal key n°"+strconv.Itoa(currentPkId)+" ! " + err2.Error())
			return nil, err2
		}

		publicKeys = append(publicKeys, publicKey)

		currentByte += 4 + keyLength
		currentPkId += 1
	}

	return publicKeys, nil
}