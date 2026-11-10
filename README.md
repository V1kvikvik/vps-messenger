A small self-hosted messenger. It has a Go backend, real-time group chats over WebSockets and PostgreSQL storage, and it is built to run on a single VPS
Features:
• Account registration and login (bcrypt password hashing, HS256 JWT)
• Group chats with owner / admin / member roles
• Real-time messaging over WebSockets, with message history
• Friend requests (send, accept, reject)
• Per-IP rate limiting on every endpoint

Tech stack:
Backend - Go, HTTPS, WebSocket, JWT
Database - PostgreSQL
Frontend - Vanilla JavaScript, HTML, CSS

Technical highlights:
• WebSocket hub. It tracks live connections under a mutex and serializes writes per connection. Ping/pong and read/write deadlines drop dead clients.
• Security basics. Every SQL query is parameterized. The JWT signing method is pinned to HS256, request bodies have a size limit, and the server checks chat membership on every read and write.
• Rate limiting. A token bucket per client IP (golang.org/x/time/rate). X-Real-IP is trusted only when the request comes from the local reverse proxy.
• Deployment. The app binds to 127.0.0.1 and expects a TLS-terminating proxy (Caddy or nginx) in front of it. It refuses to start without a strong SECRET_KEY.

Running locally:
Requirements: Go 1.26.4+ and PostgreSQL
1. Clone the repo:
git clone https://github.com/V1kvikvik/vps-messenger.git
cd vps-messenger
2. Create a .env file in the project root:
DATABASE_URL=postgres://user:password@localhost:5432/messenger
SECRET_KEY=<at least 32 bytes of cryptographically secure random key material>
3. Create the database tables (schema below), then start the server:
#MacOS: ./run.sh          # Windows: ./run.ps1
4. Open http://localhost:8080

Database schema:

CREATE TABLE users (
  id SERIAL PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash BYTEA NOT NULL
);
CREATE TABLE chats (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL
);
CREATE TABLE chat_members (
  chat_id INT REFERENCES chats(id) ON DELETE CASCADE,
  user_id INT REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL DEFAULT 'member',
  PRIMARY KEY (chat_id, user_id)
);
CREATE TABLE messages (
  id BIGSERIAL PRIMARY KEY,
  chat_id INT REFERENCES chats(id) ON DELETE CASCADE,
  sender_id INT REFERENCES users(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE friend_requests (
  requester_id INT REFERENCES users(id) ON DELETE CASCADE,
  addressee_id INT REFERENCES users(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  PRIMARY KEY (requester_id, addressee_id)
);

Nginx configuration:
location /ws {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;

    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";

    proxy_set_header Host $host;             
    proxy_set_header X-Real-IP $remote_addr; 

    proxy_read_timeout 120s;                 
}


Roadmap
• Split main.go into packages and move auth into middleware
• Database migrations and automated tests
• Message pagination and unread indicators
• Restrict WebSocket origins and handle token expiry on the client

License
MIT
