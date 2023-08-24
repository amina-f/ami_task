package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"os"
	"strings"
)

const (
	connHost = "127.0.0.1"
	connPort = "5038"
	connType = "tcp"
	username = "amina"
	secret   = "1234"
)

type amiData struct {
	TotalUsers       []string
	TotalNumOfUsers  int
	ActiveUsers      []string
	ActiveNumOfUsers int
	ActiveCalls      map[string]string
	NumOfCalls       int
	RecentEvents     []string
}

var (
	conn     net.Conn
	connErr  error
	reader   *bufio.Reader
	ch       chan bool
	Data     *amiData
	upgrader websocket.Upgrader
)

func init() {
	conn, connErr = net.Dial(connType, connHost+":"+connPort)
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
}

func action(action string) {
	fmt.Fprint(conn, action+"\r\n")
}

func login() {
	var loginInfo string = "Action: Login\r\nUsername: " + username + "\r\nSecret: " + secret + "\r\n"
	action(loginInfo)
}

// Returns the reply from Asterisk as a string.
// The second return value is 'true' if the reply is an Event and 'false' if it is a Response.
func getReply() (string, bool) {
	var reply string = ""
	var replyType bool = true
	for {
		temp, _ := reader.ReadString('\n')
		if temp == "\r\n" {
			break
		} else if strings.Contains(temp, "Asterisk Call Manager") {
			continue
		}
		reply += temp
	}
	if strings.Contains(reply, "Response: ") {
		replyType = false
	}
	return reply, replyType
}

func eventMap(event string, mappedEvent *map[string]string) {
	event = strings.Replace(event, ": \r\n", "\tNULL\r\n", -1)
	event = strings.Replace(event, "Action: ", "\t", -1)
	event = strings.Replace(event, ": ", "\t", -1)
	f := func(c rune) bool {
		return c == '\t' || c == '\r' || c == '\n'
	}
	var eventInfo []string = strings.FieldsFunc(event, f)
	for i := 0; i < len(eventInfo); i += 2 {
		(*mappedEvent)[eventInfo[i]] = eventInfo[i+1]
	}
}

func addEvent(newEvent *string) {
	if len(Data.RecentEvents) == 10 {
		Data.RecentEvents = Data.RecentEvents[1:]
	}
	Data.RecentEvents = append(Data.RecentEvents, *newEvent)
}

func getExtWithoutChannel(user string) string {
	return strings.Split(user, "/")[1]
}

func removeUser(user string) {
	ext := getExtWithoutChannel(user)
	numOfUsers := len(Data.ActiveUsers)
	for i := 0; i < numOfUsers; i++ {
		if Data.ActiveUsers[i] == ext {
			Data.ActiveUsers[i] = Data.ActiveUsers[numOfUsers-1]
			Data.ActiveUsers = Data.ActiveUsers[:(numOfUsers - 1)]
			Data.ActiveNumOfUsers--
			return
		}
	}
}

func addUser(user string) {
	ext := getExtWithoutChannel(user)
	Data.ActiveUsers = append(Data.ActiveUsers, ext)
	Data.ActiveNumOfUsers++
}

func getUsers() {
	Data.TotalUsers = Data.TotalUsers[:0]
	Data.ActiveUsers = Data.ActiveUsers[:0]
	Data.TotalNumOfUsers = 0
	Data.ActiveNumOfUsers = 0
	action("Action: PJSIPShowEndpoints\r\n")
}

func getAmiData() {
	result, resultType := getReply()
	if resultType { // Ignoring responses, only reacting to events
		var mappedEvent map[string]string = make(map[string]string)
		eventMap(result, &mappedEvent)
		var eventType string = mappedEvent["Event"]

		switch eventType {
		case "PeerStatus":
			if mappedEvent["PeerStatus"] == "Unreachable" {
				removeUser(mappedEvent["Peer"])
				var newEvent string = "Ext " + getExtWithoutChannel(mappedEvent["Peer"]) + " has unregistered."
				addEvent(&newEvent)
			} else if mappedEvent["PeerStatus"] == "Reachable" {
				addUser(mappedEvent["Peer"])
				var newEvent string = "Ext " + getExtWithoutChannel(mappedEvent["Peer"]) + " has registered."
				addEvent(&newEvent)
			}

		case "DialBegin":
			var newEvent string = mappedEvent["CallerIDNum"] + " has dialed " + mappedEvent["DestCallerIDNum"] + "."
			addEvent(&newEvent)

		case "DialState":
			var newEvent string = "Dial status of the call from " + mappedEvent["CallerIDNum"] + " to " + mappedEvent["DestCallerIDNum"] + " changed to " + mappedEvent["DialStatus"] + "."
			addEvent(&newEvent)

		case "DialEnd":
			var newEvent string
			if mappedEvent["DialStatus"] == "ANSWER" {
				newEvent = "The call between " + mappedEvent["CallerIDNum"] + " and " + mappedEvent["ConnectedLineNum"] + " has started."
				Data.ActiveCalls[mappedEvent["Linkedid"]] = mappedEvent["CallerIDNum"] + " -> " + mappedEvent["ConnectedLineNum"]
				Data.NumOfCalls++
			} else {
				newEvent = "The call from " + mappedEvent["CallerIDNum"] + " to " + mappedEvent["ConnectedLineNum"] + " ended with dial status " + mappedEvent["DialStatus"] + "."
			}
			addEvent(&newEvent)

		case "AGIExecStart":
			var newEvent string
			if mappedEvent["Command"] == "ANSWER" {
				newEvent = "The call between " + mappedEvent["CallerIDNum"] + " and " + mappedEvent["Exten"] + " has started."
				Data.ActiveCalls[mappedEvent["Linkedid"]] = mappedEvent["CallerIDNum"] + " -> " + mappedEvent["Exten"]
				Data.NumOfCalls++
				addEvent(&newEvent)
			}

		case "Hangup":
			_, keyExists := Data.ActiveCalls[mappedEvent["Linkedid"]]
			if keyExists {
				var newEvent string = "The call between " + mappedEvent["CallerIDNum"] + " and " + mappedEvent["ConnectedLineNum"] + " has ended."
				addEvent(&newEvent)
				delete(Data.ActiveCalls, mappedEvent["Linkedid"])
				Data.NumOfCalls--
			}

		case "EndpointList":
			Data.TotalUsers = append(Data.TotalUsers, mappedEvent["ObjectName"])
			Data.TotalNumOfUsers++
			if mappedEvent["DeviceState"] != "Unavailable" {
				Data.ActiveUsers = append(Data.ActiveUsers, mappedEvent["ObjectName"])
				Data.ActiveNumOfUsers++
			}
		}
	}
}

func home(w http.ResponseWriter, r *http.Request) {
	wsServe(w, r)
}

func wsServe(w http.ResponseWriter, r *http.Request) {
	ws, wsErr := upgrader.Upgrade(w, r, nil)
	defer ws.Close()
	if wsErr != nil {
		fmt.Println(wsErr)
		return
	}
	fmt.Println("Client connected!")

	getUsers()
	quit := make(chan bool)
	go wsWrite(ws, quit)

	for {
		_, _, readErr := ws.ReadMessage()
		if readErr != nil {
			fmt.Println("Websocket error: ", readErr)
			quit <- true
			return
		}
	}
}

func wsWrite(ws *websocket.Conn, quit chan bool) {
	for {
		select {
		case <-quit:
			fmt.Println("Websocket connection closed.\n")
			return
		case <-ch:
			wsErr := ws.WriteJSON(*Data)
			if wsErr != nil {
				fmt.Println("Websocket error: ", wsErr)
				return
			}
		}
	}
}

func main() {
	defer conn.Close()
	if connErr != nil {
		fmt.Println("Error connecting:", connErr)
		os.Exit(1)
	}

	reader = bufio.NewReader(conn)
	ch = make(chan bool)
	Data = &amiData{
		TotalUsers:       make([]string, 0),
		TotalNumOfUsers:  0,
		ActiveUsers:      make([]string, 0),
		ActiveNumOfUsers: 0,
		ActiveCalls:      make(map[string]string),
		NumOfCalls:       0,
		RecentEvents:     make([]string, 0),
	}

	login()
	go func() {
		for {
			getAmiData()
			ch <- true
		}
	}()

	http.HandleFunc("/", home)
	http.HandleFunc("/ws", wsServe)
	httpErr := http.ListenAndServe(":3333", nil)
	if errors.Is(httpErr, http.ErrServerClosed) {
		fmt.Println("Server closed.")
	} else if httpErr != nil {
		fmt.Println("Error starting server: ", httpErr)
		os.Exit(1)
	}
}
