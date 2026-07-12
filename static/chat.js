function Send(){
    const sendButton = document.querySelector('#sendButton')
    sendButton.addEventListener('click', (event)=>{
        const value = document.querySelector('#messageText').value
        document.querySelector('#messageText').value = ''
        ws.send(JSON.stringify({chat_id: parseInt(currentChatId), message: value}))
    })
    
}

async function ClickRegister(){
    document.addEventListener('click', async function(event){
        const targetInstance = event.target;
        if (!targetInstance){
            return
        }
        if (targetInstance.id === 'chatCreator'){
            const chatName = prompt("Введи название чата")
            const isGroup = confirm("Это групповой чат?")
            if(chatName){
                await fetch('/chats', {
                    method: 'POST',
                    headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                    body: JSON.stringify({'name': chatName, 'isgroup': isGroup})
                })
                chatsList.innerHTML = ''
                AvailableChats()
            }
        }
        if (targetInstance.dataset.chatId){
            WriteHistory(targetInstance.dataset.chatId)
            currentChatId = targetInstance.dataset.chatId
        }
        if (targetInstance.dataset.addChatId){
            const username = prompt("Enter the username: ")
            if(username){
                fetch('/addToChat', {
                    method: 'POST',
                    headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                    body: JSON.stringify({'chat_id': parseInt(targetInstance.dataset.addChatId), 'username': username})
                })
            }
        }
        if (targetInstance.dataset.RemoveChatId){
            console.log(targetInstance.dataset.RemoveChatId)
            const username = prompt("Enter the username: ")
            if(username){
                fetch('/removeFromChat', {
                    method: 'POST',
                    headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                    body: JSON.stringify({'chat_id': parseInt(targetInstance.dataset.RemoveChatId), 'username': username})
                })
            }
        }
        
    })
}

async function WriteHistory(currentChatId) {
    async function LoadHistory() {
        messageList = await fetch(`/messages?chat_id=${currentChatId}`, {
            method: 'GET',
            headers: {'Authorization': 'Bearer ' + token}
        })
        return await messageList.json()
    }
    messagesHistory = await LoadHistory()
    messageWindow.innerHTML = ''
    for(const messag of messagesHistory){
        const newMessage = document.createElement('div')
        newMessage.textContent = messag.content
        if (messag.sender != myUsername){
            newMessage.className = 'IncomingMessage'
        } else {
            newMessage.className = 'message'
        }
        console.log(messag)
            
        messageWindow.appendChild(newMessage)
        messageWindow.scrollTop = messageWindow.scrollHeight
    }
}

async function AvailableChats() {
    async function LoadChats() {
        chatList = await fetch('/getchats', {
            method: 'GET',
            headers: {'Authorization': 'Bearer ' + token},
        })
        return await chatList.json()
    }
    chatsAvailable = await LoadChats()
    chatsList.innerHTML = ''
    console.log(chatsAvailable)
    const createButton = document.createElement('button')
    createButton.textContent = "Create new chat"
    createButton.id = 'chatCreator'
    chatsList.appendChild(createButton)
    for(const chat of chatsAvailable){
        const chatRow = document.createElement('div')

        const newChat = document.createElement('span')
        newChat.textContent = chat.chat_name
        newChat.className = 'chat'
        newChat.dataset.chatId = chat.chat_id

        
        
        chatRow.className = 'chatRow'
        chatRow.appendChild(newChat)
        if (chat.role == "owner" || chat.role == "admin"){
            const addUserBtn = document.createElement('button')
            addUserBtn.textContent = 'AddUser'
            addUserBtn.dataset.addChatId = chat.chat_id
        }
        const RemoveUserBtn = document.createElement('button')
        RemoveUserBtn.textContent = 'RemoveUser'
        RemoveUserBtn.dataset.RemoveChatId = chat.chat_id
        chatRow.appendChild(addUserBtn)
        chatRow.appendChild(RemoveUserBtn)
            

        chatsList.appendChild(chatRow)
    }
}
var currentChatId = 0
const token = localStorage.getItem('token')
if(!token){
    window.location.href = '/index.html'
}
const payload = JSON.parse(atob(token.split('.')[1]))
const myUsername = payload.username
const ws = new WebSocket('ws://localhost:8080/ws')
const messageWindow = document.querySelector('#messagesWindow')
const chatsList = document.querySelector('#chatsList')
ws.onopen = () => ws.send(token)
ws.addEventListener("message", (event) => {
    const data = JSON.parse(event.data)
    if(data.type == "message"){
        const newMessage = document.createElement('div')
        newMessage.textContent = data.content
        newMessage.className = data.sender != myUsername ? 'IncomingMessage' : 'message'
        messageWindow.appendChild(newMessage)
    } else if(data.type == "chat_update"){
        chatsList.innerHTML = ''
        AvailableChats()
    }
        
        
});

ClickRegister()
Send()
AvailableChats()
CreateChat()




