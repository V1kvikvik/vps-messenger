var currentChat = {'id':0, 'object':null}
var previousChat = {'id':0, 'object':null}
const token = localStorage.getItem('token')
if(!token){
    window.location.href = '/index.html'
}
const payload = JSON.parse(atob(token.split('.')[1]))
const myUsername = payload.username
const messageWindow = document.querySelector('#messagesWindow')
const chatsList = document.querySelector('#chatsList')

let ws
function connectWS() {
    ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws')
    ws.onopen = () => ws.send(token)
    ws.addEventListener("message", (event) => {
        const data = JSON.parse(event.data)
        console.log(data)
        if(data.type == "message"){
            const newMessage = document.createElement('div')
            const newMessageText = document.createElement('div')
            newMessageText.textContent = data.content
            if(data.sender != myUsername){
                newMessage.className = 'IncomingMessage'
                const messageSource = document.createElement('div')
                messageSource.textContent = data.sender
                messageSource.className = 'senderName'
                newMessage.appendChild(messageSource)
                newMessage.appendChild(newMessageText)
            } else {
                newMessage.className = 'message'
                newMessage.appendChild(newMessageText)
            }
            messageWindow.appendChild(newMessage)
        } else if(data.type == "chat_update"){
            chatsList.innerHTML = ''
            AvailableChats()
        }
    })
    ws.onclose = (event) => {
        console.log('WS closed, reconnecting in 2s...', event.code, event.reason)
        setTimeout(connectWS, 2000)
    }
    ws.onerror = (err) => {
        console.log('WS error:', err)
    }
}


function Send(){
    const sendButton = document.querySelector('#sendButton')
    sendButton.addEventListener('click', (event)=>{
        const value = document.querySelector('#messageText').value
        document.querySelector('#messageText').value = ''
        ws.send(JSON.stringify({chat_id: parseInt(currentChat['id']), message: value}))
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
            if(chatName){
                await fetch('/chats', {
                    method: 'POST',
                    headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                    body: JSON.stringify({'name': chatName})
                })
                chatsList.innerHTML = ''
                AvailableChats()
            }
        }
        if (targetInstance.dataset.chatId){
            WriteHistory(targetInstance.dataset.chatId)
            GetChatMembers(targetInstance.dataset.chatId)
            if (previousChat['object']) {
                previousChat['object'].className = 'chat'
            }
            currentChat['id'] = targetInstance.dataset.chatId
            currentChat['object'] = targetInstance
            targetInstance.className = 'chosenChat'
            previousChat['id'] = currentChat['id']
            previousChat['object'] = currentChat['object']
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

async function GetChatMembers(currentChatId) {
    async function GetMembers() {
        membersList = await fetch(`/members?chat_id=${currentChatId}`, {
            method: 'GET',
            headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
        })
        return await membersList.json()
    }
    const memberList = await GetMembers()
    const membersPanel = document.querySelector('#userList')
    membersPanel.innerHTML = ''
    for (const m of memberList) {
        const row = document.createElement('div')
        row.className = 'memberRow'

        const nameSpan = document.createElement('span')
        nameSpan.textContent = m.username

        const roleSpan = document.createElement('span')
        roleSpan.textContent = m.role
        roleSpan.className = 'memberRole'

        row.appendChild(nameSpan)
        row.appendChild(roleSpan)
        membersPanel.appendChild(row)
    }
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
        const newMessageText = document.createElement('div')
        newMessageText.textContent = messag.content
        if (messag.sender != myUsername){
            newMessage.className = 'IncomingMessage'
            const messageSource = document.createElement('div')
            messageSource.textContent = messag.sender
            messageSource.className = 'senderName'
            newMessage.appendChild(messageSource)
            newMessage.appendChild(newMessageText)
        } else {
            newMessage.appendChild(newMessageText)
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
        const addUserBtn = document.createElement('button')
        if (chat.role == "owner" || chat.role == "admin"){
            addUserBtn.textContent = 'AddUser'
            addUserBtn.dataset.addChatId = chat.chat_id
            chatRow.appendChild(addUserBtn)
        }
        const RemoveUserBtn = document.createElement('button')
        RemoveUserBtn.textContent = 'RemoveUser'
        RemoveUserBtn.dataset.RemoveChatId = chat.chat_id
        
        chatRow.appendChild(RemoveUserBtn)
        chatsList.appendChild(chatRow)
    }
}

connectWS()
ClickRegister()
Send()
AvailableChats()
CreateChat()




