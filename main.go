package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"
)

type FriendRequestStatusChange struct {
	Username string `json:"username"`
	Status   string `json:"status"`
}

type Friend struct {
	Addressee string `json:"addressee"`
	Requester string `json:"requester"`
	Status    string `json:"status"`
}

type GetChatMembersRequest struct {
	Chat_Id int `json:"chat_id"`
}

type ChatMember struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type ClientConn struct {
	conn      *websocket.Conn
	username  string
	rateLimit *rate.Limiter
	mu        sync.Mutex
}

type WsMessage struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Sender  string `json:"sender,omitempty"`
	ChatId  int    `json:"chat_id,omitempty"`
}

type RemoveFromChatRequest struct {
	Chat_Id  int    `json:"chat_id"`
	Username string `json:"username"`
}

type AddToChatRequest struct {
	Chat_Id  int    `json:"chat_id"`
	Username string `json:"username"`
}

type Chat struct {
	Chat_Id   int    `json:"chat_id"`
	Chat_Name string `json:"chat_name"`
	Role      string `json:"role"`
}

type Message struct {
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	SenderUser string    `json:"sender"`
}

type MessageTemplate struct {
	ChatId  int    `json:"chat_id"`
	Message string `json:"message"`
}

type GroupChatCreationRequest struct {
	Name string `json:"name"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Hub struct {
	connections map[*websocket.Conn]*ClientConn
	mu          sync.Mutex
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type visitorKey struct {
	ip          string
	limiterType string
}

var (
	visitors   = make(map[visitorKey]*visitor)
	visitorsMu sync.Mutex
)

func rateLimitMiddleware(r rate.Limit, b int, next http.HandlerFunc, limiterType string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ip := clientIP(req)
		limiter := getVisitor(ip, r, b, limiterType)
		if !limiter.Allow() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next(w, req)
	}
}

func getVisitor(ip string, r rate.Limit, b int, limiterType string) *rate.Limiter {
	visitorsMu.Lock()
	defer visitorsMu.Unlock()

	v, exists := visitors[visitorKey{ip: ip, limiterType: limiterType}]
	if !exists {
		limiter := rate.NewLimiter(r, b)
		visitors[visitorKey{ip: ip, limiterType: limiterType}] = &visitor{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	v.lastSeen = time.Now()
	return v.limiter
}

func cleanupVisitors() {
	for {
		time.Sleep(time.Minute)
		visitorsMu.Lock()
		for ip, v := range visitors {
			if time.Since(v.lastSeen) > 3*time.Minute {
				delete(visitors, ip)
			}
		}
		visitorsMu.Unlock()
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	if host == "127.0.0.1" || host == "::1" {
		forwarded := r.Header.Get("X-Real-IP")

		if ip := net.ParseIP(forwarded); ip != nil {
			return ip.String()
		}
	}

	return host
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
}
var dbpool, err = pgxpool.New(context.Background(), connStr)
var secretKey = []byte(os.Getenv("SECRET_KEY"))
var connStr = os.Getenv("DATABASE_URL")

func userIsInChat(ctx context.Context, username string, chatId int) (bool, error) {
	var exists bool
	err := dbpool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM chat_members cm
			JOIN users u ON u.id = cm.user_id
			WHERE u.username = $1 AND cm.chat_id = $2
		)`, username, chatId,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (h *Hub) Add(clientConn *ClientConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connections[clientConn.conn] = clientConn
}

func (h *Hub) Remove(name *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.connections, name)
}
func (h *Hub) Broadcast(typ int, message []byte, chatid int) {
	UserRows, DBerr := dbpool.Query(context.Background(), "SELECT u.username FROM users u JOIN chat_members cm ON u.id = cm.user_id WHERE cm.chat_id = $1", chatid)
	if DBerr != nil {
		log.Println("Error when getting values from DB: ", DBerr)
		return
	}
	var users []string
	for UserRows.Next() {
		var username string
		Geterr := UserRows.Scan(&username)
		if Geterr != nil {
			log.Println("Can`t get username from DB: ", Geterr)
			UserRows.Close()
			return
		}
		users = append(users, username)
	}
	if UserRows.Err() != nil {
		log.Println("Error while iterating over user rows: ", UserRows.Err())
		UserRows.Close()
		return
	}
	UserRows.Close()
	for _, user := range users {
		h.SendToUser(user, typ, message)
	}
}

const maxBodyBytes = 1 << 20

func recoverMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("Recovered from panic in %s: %v", r.URL.Path, rec)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next(w, r)
	}
}

func isValidUsername(u string) bool {
	if len(u) < 3 || len(u) > 32 {
		return false
	}
	for _, r := range u {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func isValidPassword(p string) bool {
	return len(p) >= 8 && len(p) <= 72 // bcrypt молча обрезает пароли длиннее 72 байт
}

func (h *Hub) SendToUser(username string, typ int, message []byte) {
	h.mu.Lock()
	var connection_safe_pointer []*ClientConn
	for _, value := range h.connections {
		if value.username == username {
			connection_safe_pointer = append(connection_safe_pointer, value)
		}
	}
	h.mu.Unlock()
	if len(connection_safe_pointer) == 0 {
		log.Println("No connections found for user:", username)
		return
	}
	for _, conn := range connection_safe_pointer {
		err := conn.SafeWrite(typ, message)
		if err != nil {
			log.Println("SafeWrite failed during SendToUser for user", username, ":", err)
			h.Remove(conn.conn)
			continue
		}
	}
}

func (h *Hub) SendToEveryone(typ int, message []byte) {
	h.mu.Lock()
	var users []*ClientConn
	for _, value := range h.connections {
		users = append(users, value)
	}
	h.mu.Unlock()
	for _, user := range users {
		err := user.SafeWrite(typ, message)
		if err != nil {
			log.Println("SafeWrite failed during SendToEveryone for user", user.username, ":", err)
			h.Remove(user.conn)
			continue
		}
	}
}

func (c *ClientConn) SafeWrite(typ int, message []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		return err
	}
	possibleErr := c.conn.WriteMessage(typ, message)
	if possibleErr != nil {
		return possibleErr
	}
	return nil
}

func main() {
	if len(secretKey) == 0 {
		log.Fatal("SECRET_KEY is not set - refusing to start")
	}
	if len(secretKey) < 32 {
		log.Fatal("SECRET_KEY is too short - must be at least 32 bytes")
	}
	if connStr == "" {
		log.Fatal("DATABASE_URL is not set - refusing to start")
	}
	if err != nil {
		fmt.Println("The database is not accessible", err)
		return
	}
	dberr := dbpool.Ping(context.Background())
	if dberr != nil {
		fmt.Println("The connection with database went wrong", dberr)
		return
	}
	defer dbpool.Close()
	go cleanupVisitors()
	mainHub := Hub{connections: make(map[*websocket.Conn]*ClientConn)}
	http.HandleFunc("/ws", rateLimitMiddleware(2, 5, func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("The error occured: ", err)
			return
		}
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		conn.SetReadLimit(16384)

		defer func() {
			if rec := recover(); rec != nil {
				log.Println("Recovered from panic in WS handler:", rec)
			}
			conn.Close()
		}()

		_, firstMessage, readErr := conn.ReadMessage()
		if readErr != nil {
			log.Println("Something went wrong while reading a message: ", readErr)
			return
		}
		token := string(firstMessage)
		var claims jwt.MapClaims
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			errControl := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Authentication failed"), time.Now().Add(10*time.Second))
			conn.Close()
			if errControl != nil {
				log.Println("Error sending close message:", errControl)
			}
			return
		}

		usernameVal, ok := claims["username"].(string)
		if !ok || usernameVal == "" {
			log.Println("Token missing valid username claim")
			return
		}

		var user_id int
		if scanErr := dbpool.QueryRow(context.Background(),
			"SELECT id FROM users WHERE username = ($1)", usernameVal,
		).Scan(&user_id); scanErr != nil {
			log.Println("User not found in DB:", scanErr)
			return
		}
		errDeadline := conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		if errDeadline != nil {
			log.Println("SetReadDeadline error:", errDeadline)
			return
		}
		var userConn *ClientConn
		userConn = &ClientConn{conn: conn, username: usernameVal, rateLimit: rate.NewLimiter(rate.Limit(5), 10)}
		connSuccesfull, mrshlerr := json.Marshal(WsMessage{Type: "auth_success", Content: "Authentication successful"})
		if mrshlerr != nil {
			log.Println("Error marshalling auth_success message:", mrshlerr)
			return
		}
		sfwrterr := userConn.SafeWrite(1, connSuccesfull)
		if sfwrterr != nil {
			log.Println("Error sending auth_success message:", sfwrterr)
			errControl := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1011, "Authentication failed"), time.Now().Add(10*time.Second))
			if errControl != nil {
				log.Println("Error sending close message:", errControl)
			}
			return
		}

		var expiration, expErr = claims.GetExpirationTime()
		if expErr != nil || expiration == nil {
			log.Println("There is no valid expiration time for a token")
			return
		}
		expirationDuration := time.Until(expiration.Time)
		if expirationDuration <= 0 {
			log.Println("Token has expired")
			errControl := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Token has expired"), time.Now().Add(10*time.Second))
			if errControl != nil {
				log.Println("Error sending close message:", errControl)
			}
			return
		}
		var done = make(chan struct{})
		var expirationTimer = time.NewTimer(expirationDuration)
		go func() {
			select {
			case <-expirationTimer.C:
				log.Println("JWT expiration timer fired")
				errControlExp := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Token has expired"), time.Now().Add(10*time.Second))
				conn.Close()
				if errControlExp != nil {
					log.Println("Something went wrong during websocket close-frame sending in expiration goroutine", errControlExp)
				}
				return
			case <-done:
				return
			}
		}()
		defer expirationTimer.Stop()
		var ping = time.NewTicker(time.Second * 30)

		defer close(done)
		conn.SetPongHandler(func(appData string) error {
			err := conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			if err != nil {
				log.Println("SetReadDeadline error:", err)
				return err
			}
			return nil
		})
		go func() {
			defer ping.Stop()
			for {
				select {
				case <-ping.C:
					err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
					if err != nil {
						log.Println("Ping error:", err)
						conn.Close()
						return
					}
				case <-done:
					return
				}
			}
		}()
		mainHub.Add(userConn)
		defer mainHub.Remove(conn)

		for {
			var req MessageTemplate
			typ, message, errorCatch := conn.ReadMessage()
			if errorCatch != nil {
				log.Println("The error occured: ", errorCatch)
				return
			}
			allowance := userConn.rateLimit.Allow()
			if allowance == false {
				log.Println("Rate limit exceeded")
				continue
			}
			if MarshalErr := json.Unmarshal(message, &req); MarshalErr != nil {
				log.Println("Something went wrong during unmarshalling: ", MarshalErr)
				continue
			}
			req.Message = strings.TrimSpace(req.Message)
			if req.Message == "" || len(req.Message) > 4000 || req.ChatId <= 0 {
				errorMsg, err := json.Marshal(WsMessage{Type: "error", Content: "message too long"})
				if err != nil {
					log.Println("Something went wrong when marshalling error message: ", err)
					return
				}
				userConn.SafeWrite(1, errorMsg)
				continue
			}
			member, memErr := userIsInChat(context.Background(), usernameVal, req.ChatId)
			if memErr != nil {
				log.Println("Error checking chat membership:", memErr)
				continue
			}
			if !member {
				log.Println("User", usernameVal, "attempted to send a message to a chat they are not a member of:", req.ChatId)
				continue
			}

			_, MessageErr := dbpool.Exec(context.Background(),
				"INSERT INTO messages (sender_id, content, chat_id) VALUES ($1, $2, $3)",
				user_id, req.Message, req.ChatId)
			if MessageErr != nil {
				log.Println("Something went wrong when parsing message into DB: ", MessageErr)
				continue
			}

			jsonMsg, _ := json.Marshal(WsMessage{Type: "message", Content: req.Message, Sender: usernameVal, ChatId: req.ChatId})
			mainHub.Broadcast(typ, jsonMsg, req.ChatId)
		}
	}, "websocket"))
	http.HandleFunc("/register", rateLimitMiddleware(2, 10, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var req RegisterRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !isValidUsername(req.Username) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !isValidPassword(req.Password) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		encrPass, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			fmt.Println("Something went wrong during password encryption: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, dbInserterr := dbpool.Exec(context.Background(), "INSERT INTO users (username, password_hash) VALUES ($1, $2)", req.Username, encrPass)
		if dbInserterr != nil {
			fmt.Println("Something went wrong when parsing to DB: ", dbInserterr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}), "auth"))
	http.HandleFunc("/login", rateLimitMiddleware(2, 10, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var req LoginRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Password == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var row string
		Loginerr := dbpool.QueryRow(context.Background(), "SELECT password_hash FROM users WHERE username = $1", req.Username).Scan(&row)
		if Loginerr != nil {
			log.Println("There is no such user in db")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(row), []byte(req.Password)) == nil {
			log.Println("Login successful")
		} else {
			log.Println("Wrong password")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := jwt.MapClaims{
			"username": req.Username,
			"exp":      time.Now().Add(15 * time.Second).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tok, tokerr := token.SignedString([]byte(secretKey))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(tok))
	}), "auth"))
	http.HandleFunc("/chats", rateLimitMiddleware(2, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var req GroupChatCreationRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 100 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var Chatid int
		var Userid int
		tx, txErr := dbpool.Begin(context.Background())
		if txErr != nil {
			log.Println("Something went wrong when making pool connection to DB: ", txErr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		defer tx.Rollback(context.Background())
		dbInserterr := tx.QueryRow(context.Background(), "INSERT INTO chats (name) VALUES ($1) RETURNING id", req.Name).Scan(&Chatid)
		if dbInserterr != nil {
			fmt.Println("Something went wrong when parsing to DB: ", dbInserterr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		dbuserselect := tx.QueryRow(context.Background(), "SELECT id FROM users WHERE username = ($1)", claims["username"]).Scan(&Userid)
		if dbuserselect != nil {
			fmt.Println("Something went wrong when parsing to DB: ", dbuserselect)
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, ChatMembersErr := tx.Exec(context.Background(), "INSERT INTO chat_members (chat_id, user_id, role) VALUES ($1, $2, $3)", Chatid, Userid, "owner")
		if ChatMembersErr != nil {
			fmt.Println("Something wrong when parsing to DB", ChatMembersErr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		jsonMsg, _ := json.Marshal(WsMessage{Type: "chat_update"})
		commitErr := tx.Commit(context.Background())
		if commitErr != nil {
			log.Println("Something went wrong when committing to DB: ", commitErr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		mainHub.SendToEveryone(1, jsonMsg)

	}), "write"))
	http.HandleFunc("/messages", rateLimitMiddleware(5, 20, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		usernameVal, ok := claims["username"].(string)
		if !ok || usernameVal == "" {
			log.Println("Token missing valid username claim")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		chatIdStr := r.URL.Query().Get("chat_id")
		chatId, convErr := strconv.Atoi(chatIdStr)
		if convErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		member, memErr := userIsInChat(r.Context(), usernameVal, chatId)
		if memErr != nil {
			log.Println("Error checking chat membership:", memErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !member {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		messages, readErr := dbpool.Query(r.Context(),
			"SELECT content, messages.created_at, users.username FROM messages JOIN users ON messages.sender_id = users.id WHERE chat_id = $1 ORDER BY created_at DESC LIMIT 50",
			chatId)
		if readErr != nil {
			log.Println("Something went wrong when reading messages: ", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		messagesArray := make([]Message, 0, 50)
		defer messages.Close()
		for messages.Next() {
			var temp string
			var createdAt time.Time
			var sender string
			if err := messages.Scan(&temp, &createdAt, &sender); err != nil {
				log.Println("Something went wrong when scanning message: ", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			messagesArray = append(messagesArray, Message{temp, createdAt, sender})
		}
		messagesErr := messages.Err()
		if messagesErr != nil {
			log.Println("Something went wrong while iterating over messages: ", messagesErr)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		slices.Reverse(messagesArray)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(messagesArray)
	}), "read"))
	http.HandleFunc("/getchats", rateLimitMiddleware(5, 20, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var userId int
		dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = $1", claims["username"]).Scan(&userId)
		chatRows, chaterr := dbpool.Query(context.Background(), "SELECT chats.id, chats.name, chat_members.role FROM chat_members JOIN chats ON chat_members.chat_id = chats.id WHERE chat_members.user_id = $1", userId)
		if chaterr != nil {
			log.Println("Something went wrong while reading available chats: ", chaterr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		idArr := []Chat{}
		for chatRows.Next() {
			var temp int
			var chat_name string
			var role string
			if err := chatRows.Scan(&temp, &chat_name, &role); err != nil {
				log.Println("Something went wrong when scanning chat_ids: ", err)
				continue
			}
			idArr = append(idArr, Chat{temp, chat_name, role})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(idArr)

	}), "read"))
	http.HandleFunc("/addToChat", rateLimitMiddleware(2, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var req AddToChatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Chat_Id <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var role string
		scErr := dbpool.QueryRow(context.Background(), "SELECT role FROM chat_members JOIN users ON chat_members.user_id = users.id WHERE users.username = $1 AND chat_members.chat_id = $2", claims["username"], req.Chat_Id).Scan(&role)
		if scErr != nil {
			log.Println("Something went wrong while getting user`s role: ", scErr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if role != "owner" && role != "admin" {
			log.Println("User", claims["username"], "attempted to add a member to chat", req.Chat_Id, "without sufficient privileges")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var userId int
		userErr := dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = $1", req.Username).Scan(&userId)
		if userErr != nil {
			log.Println("User not found:", req.Username, userErr)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, parseErr := dbpool.Exec(context.Background(), "INSERT INTO chat_members (chat_id, user_id) VALUES($1, $2)", req.Chat_Id, userId)
		if parseErr != nil {
			log.Println("Something went wrong when inserting chat member: ", parseErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		jsonMsg, _ := json.Marshal(WsMessage{Type: "chat_update"})
		mainHub.SendToEveryone(1, jsonMsg)
	}), "write"))
	http.HandleFunc("/removeFromChat", rateLimitMiddleware(2, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var req RemoveFromChatRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Chat_Id <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var role string
		var tryersRole string
		scErrTryer := dbpool.QueryRow(context.Background(), "SELECT role FROM chat_members JOIN users ON chat_members.user_id = users.id WHERE users.username = $1 AND chat_members.chat_id = $2", claims["username"], req.Chat_Id).Scan(&tryersRole)
		if scErrTryer != nil {
			log.Println("Something went wrong while getting trying user`s role: ", scErrTryer)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		scErr := dbpool.QueryRow(context.Background(), "SELECT role FROM chat_members JOIN users ON chat_members.user_id = users.id WHERE users.username = $1 AND chat_members.chat_id = $2", req.Username, req.Chat_Id).Scan(&role)
		if scErr != nil {
			log.Println("Something went wrong while getting user`s role: ", scErr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if (tryersRole == "member" && req.Username != claims["username"]) || (tryersRole == "admin" && role != "member" && req.Username != claims["username"]) || (tryersRole == "owner" && req.Username == claims["username"]) {
			log.Println("User", claims["username"], "attempted to remove a member from chat", req.Chat_Id, "without sufficient privileges")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var userId int
		scanErr := dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = $1", req.Username).Scan(&userId)
		if scanErr != nil {
			log.Println("Something went wrong while getting userId: ", scanErr)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, execErr := dbpool.Exec(context.Background(), "DELETE FROM chat_members WHERE chat_id = $1 AND user_id = $2", req.Chat_Id, userId)
		log.Println("Trying to delete user with id ", userId, " from chat with id ", req.Chat_Id)
		if execErr != nil {
			log.Println("Something went wrong while deleting user: ", execErr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		jsonMsg, _ := json.Marshal(WsMessage{Type: "chat_update"})
		mainHub.SendToEveryone(1, jsonMsg)
	}), "write"))

	http.HandleFunc("/members", rateLimitMiddleware(5, 20, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		chatId, intErr := strconv.Atoi(r.URL.Query().Get("chat_id"))
		if intErr != nil {
			log.Println("Error while getting chat id: ", intErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		username, ok := claims["username"].(string)
		if !ok || username == "" {
			log.Println("Token missing valid username claim")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		member, memberErr := userIsInChat(r.Context(), username, chatId)
		if memberErr != nil {
			log.Println("Something went during the user check: ", memberErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !member {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		rows, queryErr := dbpool.Query(context.Background(), "SELECT u.username, cm.role FROM chat_members cm JOIN users u ON u.id = cm.user_id WHERE cm.chat_id = $1", chatId)
		if queryErr != nil {
			log.Println("Something went wrong while getting chat members")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		memberArray := make([]ChatMember, 0)
		for rows.Next() {
			var m ChatMember
			if err := rows.Scan(&m.Username, &m.Role); err != nil {
				log.Println("Something went wrong when scanning chat member: ", err)
				continue
			}
			memberArray = append(memberArray, m)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(memberArray)

	}), "read"))
	http.HandleFunc("/friendRequest", rateLimitMiddleware(2, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		username, ok := claims["username"].(string)
		if !ok || username == "" {
			log.Println("Token missing valid username claim")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var requesterId int
		dbuserselect := dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = ($1)", username).Scan(&requesterId)
		if dbuserselect != nil {
			fmt.Println("Something went wrong when parsing to DB: ", dbuserselect)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var addresseeId int
		addresseeErr := dbpool.QueryRow(r.Context(), "SELECT id FROM users WHERE username = ($1)", r.URL.Query().Get("addresseeUsername")).Scan(&addresseeId)
		if addresseeErr != nil {
			log.Println("Error occured while getting the friend request addressee`s userid", addresseeErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var exists bool
		matchingRowsErr := dbpool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM friend_requests WHERE (requester_id = $1 AND addressee_id = $2) OR (requester_id = $2 AND addressee_id = $1))", requesterId, addresseeId).Scan(&exists)
		if matchingRowsErr != nil {
			log.Println("Error occured while checking the id pair in friend_requests", matchingRowsErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if exists == true {
			log.Println("The user is already a friend/The request is already pending")
			w.WriteHeader(http.StatusBadRequest)
			return
		} else {
			_, err := dbpool.Exec(r.Context(), "INSERT INTO friend_requests (requester_id, addressee_id, status) VALUES ($1, $2, $3)", requesterId, addresseeId, "Pending")
			if err != nil {
				log.Println("Error while creating friend request:", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			var friend Friend
			friend.Addressee = r.URL.Query().Get("addresseeUsername")
			friend.Status = "Pending"
			friend.Requester = username
			w.WriteHeader(http.StatusCreated)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(friend)
		}

	}), "write"))
	http.HandleFunc("/friendsList", rateLimitMiddleware(5, 20, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		username, ok := claims["username"].(string)
		if !ok || username == "" {
			log.Println("Token missing valid username claim")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var requesterId int
		dbuserselect := dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = ($1)", username).Scan(&requesterId)
		if dbuserselect != nil {
			fmt.Println("Something went wrong when parsing to DB: ", dbuserselect)
			w.WriteHeader(http.StatusConflict)
			return
		}
		friendsList, friendsListErr := dbpool.Query(r.Context(), `SELECT
								requester.username AS requester_username,
								addressee.username AS addressee_username,
								fr.status
							FROM friend_requests fr
							JOIN users requester
								ON requester.id = fr.requester_id
							JOIN users addressee
								ON addressee.id = fr.addressee_id
							WHERE fr.requester_id = $1
							OR fr.addressee_id = $1;`, requesterId)
		if friendsListErr != nil {
			log.Println("Something went wrong while getting the friend_request", friendsListErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		friendsArr := make([]Friend, 0)
		for friendsList.Next() {
			var friend Friend
			if err := friendsList.Scan(&friend.Requester, &friend.Addressee, &friend.Status); err != nil {
				log.Println("Something went wrong when scanning friend requests: ", err)
				continue
			}
			friendsArr = append(friendsArr, friend)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(friendsArr)
		defer friendsList.Close()
	}), "read"))
	http.HandleFunc("/friendsChangeStatus", rateLimitMiddleware(2, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
		var claims jwt.MapClaims
		token := r.Header.Get("Authorization")
		token = strings.TrimPrefix(token, "Bearer ")
		_, tokerr := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return secretKey, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		username, ok := claims["username"].(string)
		if !ok || username == "" {
			log.Println("Token missing valid username claim")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var requesterId int
		dbuserselect := dbpool.QueryRow(context.Background(), "SELECT id FROM users WHERE username = ($1)", username).Scan(&requesterId)
		if dbuserselect != nil {
			fmt.Println("Something went wrong when parsing requesterId from DB: ", dbuserselect)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req FriendRequestStatusChange
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			fmt.Println("The error occured with json decoding: ", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var changeStatus = req.Status
		var addresseeId int
		dbuserselectaddressee := dbpool.QueryRow(r.Context(), "SELECT id FROM users WHERE username = ($1)", req.Username).Scan(&addresseeId)
		if dbuserselectaddressee != nil {
			log.Println(
				"Something went wrong when parsing addresseeId from DB: ",
				dbuserselectaddressee,
			)
		}
		switch changeStatus {
		case "Accepted":
			_, err := dbpool.Exec(r.Context(), "UPDATE friend_requests SET status='Accepted' WHERE requester_id = $1 AND addressee_id = $2 AND status = 'Pending'", addresseeId, requesterId)
			if err != nil {
				log.Println("Error while accepting friend request:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		case "Rejected":
			_, err := dbpool.Exec(r.Context(), "DELETE FROM friend_requests WHERE (requester_id = $1 AND addressee_id = $2) OR (requester_id = $2 AND addressee_id = $1)", addresseeId, requesterId)
			if err != nil {
				log.Println("Error while accepting friend request:", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		payload, marshalErr := json.Marshal(WsMessage{Type: "friend_request_update"})
		if marshalErr != nil {
			log.Println("Something went wrong while marshaling friend_request_update: ", marshalErr)
			return
		}
		mainHub.SendToUser(username, websocket.TextMessage, payload)
	}), "write"))

	http.Handle("/", http.FileServer(http.Dir("static")))
	srv := http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           nil,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errListen := srv.ListenAndServe()
	if errListen != nil {
		log.Fatal("ListenAndServe: ", errListen)
	}

}
