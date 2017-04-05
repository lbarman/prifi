package relay

import (
	"time"

	"gopkg.in/dedis/onet.v1/log"
	"strconv"
	"github.com/lbarman/prifi/prifi-lib/net"
)

/*
This first timeout happens after a short delay. Clients will not be considered disconnected yet,
but if we use UDP, it can mean that a client missed a broadcast, and we re-sent the message.
If the round was *not* done, we do another timeout (Phase 2), and then, clients/trustees will be considered
online if they didn't answer by that time.
*/
func (p *PriFiLibRelayInstance) checkIfRoundHasEndedAfterTimeOut_Phase1(roundID int32) {

	time.Sleep(TIMEOUT_PHASE_1)

	if !p.relayState.dcnetRoundManager.CurrentRoundIsStill(roundID) {
		return //everything went well, it's great !
	}

	if p.stateMachine.State() == "SHUTDOWN" {
		return //nothing to ensure in that case
	}

	log.Error("waitAndCheckIfClientsSentData : We seem to be stuck in round", roundID, ". Phase 1 timeout.")

	missingClientCiphers, missingTrusteesCiphers := p.relayState.bufferManager.MissingCiphersForCurrentRound()

	//If we're using UDP, client might have missed the broadcast, re-sending
	if p.relayState.UseUDP {
		/*
		for clientID := range missingClientCiphers {
			log.Error("Relay : Client " + strconv.Itoa(clientID) + " didn't sent us is cipher for round " + strconv.Itoa(int(roundID)) + ". Phase 1 timeout. Re-sending...")
			extraInfo := "(client " + strconv.Itoa(clientID) + ", round " + strconv.Itoa(int(roundID)) + ")"
			dataAlreadySent := p.relayState.dcnetRoundManager.GetDataAlreadySent(roundID)
			p.messageSender.SendToClientWithLog(clientID, dataAlreadySent, extraInfo)
		}
		*/
		log.Error("Relay : Clients", missingClientCiphers, "didn't sent us is cipher for round " + strconv.Itoa(int(roundID)) + ". Phase 1 timeout. Re-sending...")
		dataAlreadySent := p.relayState.dcnetRoundManager.GetDataAlreadySent(roundID)
		toSend := &net.REL_CLI_DOWNSTREAM_DATA_UDP{REL_CLI_DOWNSTREAM_DATA: *dataAlreadySent}
		p.messageSender.BroadcastToAllClientsWithLog(toSend, "(UDP retransmission, round "+strconv.Itoa(int(p.relayState.nextDownStreamRoundToSend))+")")

		p.relayState.bitrateStatistics.AddDownstreamRetransmitCell(int64(len(dataAlreadySent.Data)))
	}

	if len(missingClientCiphers) > 0 || len(missingTrusteesCiphers) > 0 {
		//if we're not done (we miss data), wait another timeout, after which clients/trustees will be considered offline
		go p.checkIfRoundHasEndedAfterTimeOut_Phase2(roundID)
	}

	//this shouldn't happen frequently (it means that the timeout 1 was fired, but the round finished almost at the same time)
}

/*
This second timeout happens after a longer delay. Clients and trustees will be considered offline if they haven't send data yet
*/
func (p *PriFiLibRelayInstance) checkIfRoundHasEndedAfterTimeOut_Phase2(roundID int32) {

	time.Sleep(TIMEOUT_PHASE_2)

	if !p.relayState.dcnetRoundManager.CurrentRoundIsStill(roundID) {
		//everything went well, it's great !
		return
	}

	if p.stateMachine.State() == "SHUTDOWN" {
		//nothing to ensure in that case
		return
	}

	log.Error("waitAndCheckIfClientsSentData : We seem to be stuck in round", roundID, ". Phase 2 timeout.")

	log.Lvl3("Stopping experiment, if any.")
	output := p.relayState.ExperimentResultData
	output = append(output, "!!aborted-round-"+strconv.Itoa(int(roundID)))
	p.relayState.ExperimentResultChannel <- output

	missingClientCiphers, missingTrusteesCiphers := p.relayState.bufferManager.MissingCiphersForCurrentRound()
	p.relayState.timeoutHandler(missingClientCiphers, missingTrusteesCiphers)
}
