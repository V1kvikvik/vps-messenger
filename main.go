package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
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

type GetChatMembersRequest struct {
	Chat_Id int `json:"chat_id"`
}

type ChatMember struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type ClientConn struct {
	conn     *websocket.Conn
	username string
	mu       sync.Mutex
}

type WsMessage struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Sender  string `json:"sender,omitempty"`
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

var (
	visitors   = make(map[string]*visitor)
	visitorsMu sync.Mutex
)

func rateLimitMiddleware(r rate.Limit, b int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ip := clientIP(req)
		limiter := getVisitor(ip, r, b)
		if !limiter.Allow() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next(w, req)
	}
}

func getVisitor(ip string, r rate.Limit, b int) *rate.Limiter {
	visitorsMu.Lock()
	defer visitorsMu.Unlock()

	v, exists := visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(r, b)
		visitors[ip] = &visitor{limiter: limiter, lastSeen: time.Now()}
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
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Не забудь изменить перед запуском!!!
var upgrader = websocket.Upgrader{
	ReadBufferSize:  2048,
	WriteBufferSize: 2048,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
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

func (h *Hub) Add(name *websocket.Conn, username string) *ClientConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	clientConn := &ClientConn{conn: name, username: username}
	h.connections[name] = clientConn
	return clientConn
}

func (h *Hub) Remove(name *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.connections, name)
}
func (h *Hub) Broadcast(typ int, message []byte, chatid int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	UserRows, DBerr := dbpool.Query(context.Background(), "SELECT u.username FROM users u JOIN chat_members cm ON u.id = cm.user_id WHERE cm.chat_id = $1", chatid)
	if DBerr != nil {
		log.Println("Error when getting values from DB: ", DBerr)
		return
	}
	defer UserRows.Close()
	for UserRows.Next() {
		var username string
		Geterr := UserRows.Scan(&username)
		if Geterr != nil {
			log.Println("Can`t get username from DB: ", Geterr)
			return
		}
		for key, value := range h.connections {
			if value.username == username {
				err := value.SafeWrite(typ, message)
				if err != nil {
					log.Println("Something bad happened: ", err)
					delete(h.connections, key)
				}
			}
		}
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

func (h *Hub) SendToEveryone(typ int, message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, value := range h.connections {
		err := value.SafeWrite(typ, message)
		if err != nil {
			log.Println("Something bad happened: ", err)
			delete(h.connections, key)
		}
	}
}

func (c *ClientConn) SafeWrite(typ int, message []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
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

		currentConn := mainHub.Add(conn, usernameVal)
		defer mainHub.Remove(conn)

		for {
			var req MessageTemplate
			typ, message, errorCatch := conn.ReadMessage()
			if errorCatch != nil {
				log.Println("The error occured: ", errorCatch)
				return
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
				currentConn.SafeWrite(1, errorMsg)
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

			jsonMsg, _ := json.Marshal(WsMessage{Type: "message", Content: req.Message, Sender: usernameVal})
			mainHub.Broadcast(typ, jsonMsg, req.ChatId)
		}
	}))
	http.HandleFunc("/register", rateLimitMiddleware(1, 3, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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
	})))
	http.HandleFunc("/login", rateLimitMiddleware(1, 5, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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
			"exp":      time.Now().Add(24 * time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tok, tokerr := token.SignedString([]byte(secretKey))
		if tokerr != nil {
			log.Println("Something went during the token check: ", tokerr)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(tok))
	})))
	http.HandleFunc("/chats", rateLimitMiddleware(3, 10, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

	})))
	http.HandleFunc("/messages", rateLimitMiddleware(5, 15, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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
			"SELECT content, messages.created_at, users.username FROM messages JOIN users ON messages.sender_id = users.id WHERE chat_id = $1 ORDER BY created_at ASC LIMIT 50",
			chatId)
		if readErr != nil {
			log.Println("Something went wrong when reading messages: ", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var messagesArray []Message
		defer messages.Close()
		for messages.Next() {
			var temp string
			var createdAt time.Time
			var sender string
			if err := messages.Scan(&temp, &createdAt, &sender); err != nil {
				log.Println("Something went wrong when scanning message: ", err)
				continue
			}
			messagesArray = append(messagesArray, Message{temp, createdAt, sender})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(messagesArray)
	})))
	http.HandleFunc("/getchats", rateLimitMiddleware(5, 15, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

	})))
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
	})))
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
	})))

	http.HandleFunc("/members", rateLimitMiddleware(3, 10, recoverMiddleware(func(w http.ResponseWriter, r *http.Request) {
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

		var memberArray []ChatMember
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

	})))

	http.Handle("/", http.FileServer(http.Dir("static")))
	http.ListenAndServe(":8080", nil)

}
