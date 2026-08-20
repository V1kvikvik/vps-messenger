var currentChat = {'id':0, 'object':null}
var previousChat = {'id':0, 'object':null}
var friends = []
const token = localStorage.getItem('token')
if(!token){
    window.location.href = '/index.html'
}
const payload = JSON.parse(atob(token.split('.')[1]))
const myUsername = payload.username
const messageWindow = document.querySelector('#messagesWindow')
const chatsList = document.querySelector('#chatsList')
var currentChatSelectionMode = 'FriendList'

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
        } else if(data.type == "friend_request_update"){
            GetFriendList()
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
    const messageInput = document.querySelector('#messageText')
    const value = messageInput.value
    if (value.trim() === '') return
    messageInput.value = ''
    ws.send(JSON.stringify({chat_id: parseInt(currentChat['id']), message: value}))
}
function SendFriendRequest(){
    const usernameInput = document.querySelector('#friendRequestField')
    const value = usernameInput.value
    if (value.trim() === '') return
    usernameInput.value = ''
    FriendRequest(value)
    console.log("Tried to send friend request")
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
        if (targetInstance.id === 'userListLabel'){
            const membersButton = document.querySelector('#userListLabel')
            const friendsButton = document.querySelector('#friendListLabel')
            membersButton.classList.add('active')
            friendsButton.classList.remove('active')
            currentChatSelectionMode = 'ChatMembers'
            const userList = document.getElementById('userList')
            userList.style.width = "13%"
            GetChatMembers(currentChat['id'])
        }
        if (targetInstance.id === 'friendListLabel'){
            const userList = document.getElementById('userList')
            const listsHolder = document.querySelector('#ListsHolder')
            const membersButton = document.querySelector('#userListLabel')
            const friendsButton = document.querySelector('#friendListLabel')
            friendsButton.classList.add('active')
            membersButton.classList.remove('active')
            currentChatSelectionMode = 'FriendList'
            listsHolder.innerHTML = `
             <div id="friendsList">
                <div class="FriendInputHolder">
                    <input
                        type="text"
                        id="friendRequestField"
                        placeholder="Enter username to add to your friend list"
                        required>
                    <button id="friendSendButton">Send request</button>
                </div>
                <div id="friends">

                </div>
                <hr>
                <span>Pending friend requests</span>
                <div id="friendRequests">
                    
                </div>
            </div>`
            userList.style.width = "26%"
            friends.forEach(element => {
               addFriend(element) 
            });
        }
        if (targetInstance.dataset.chatId){
            WriteHistory(targetInstance.dataset.chatId)
            if (currentChatSelectionMode === 'ChatMembers'){
                GetChatMembers(targetInstance.dataset.chatId)
            }
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
        if (targetInstance.classList.contains('acceptButton')){
            ChangeFriendRequestStatus(targetInstance.dataset.USERNAME, 'Accepted')
        }
        if (targetInstance.classList.contains('rejectButton')){
            ChangeFriendRequestStatus(targetInstance.dataset.USERNAME, 'Rejected')
            console.log('Tried to delete user`s request from DB: ', targetInstance.dataset.USERNAME)
        }
        
    })
}

async function ChangeFriendRequestStatus(addresseeUsername, newStatus){
    async function ChangeStatus() {
        var response = await fetch(`/friendsChangeStatus`, {
            method: 'POST',
            headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
            body: JSON.stringify({
                username: addresseeUsername,
                status: newStatus
            })
        })
        if (!response.ok) {
            console.error('Failed to change friend request status')
        }
    }
    ChangeStatus()
}

function addFriend(data){
    const friendRequestsList = document.querySelector('#friendRequests')
    
    const friendRequestContainer = document.createElement('div')
    friendRequestContainer.className = 'friendRequestContainer'

    const friendRequest = document.createElement('span')
    friendRequest.className = 'friendRequest'
    friendRequestContainer.appendChild(friendRequest)

    if(data.addressee === myUsername && data.status === 'Pending'){
        friendRequest.textContent = `${data.requester}`
        const acceptButton = document.createElement('button')
        acceptButton.className = 'acceptButton'
        acceptButton.dataset.USERNAME = data.requester
        acceptButton.style.width = '20%'
        acceptButton.textContent = 'Accept'
        friendRequestContainer.appendChild(acceptButton)

        const rejectButton = document.createElement('button')
        rejectButton.className = 'rejectButton'
        rejectButton.dataset.USERNAME = data.requester
        rejectButton.style.width = '20%'
        rejectButton.textContent = 'Reject'
        friendRequestContainer.appendChild(rejectButton)
    } else if(data.addressee != myUsername && data.status === 'Pending'){
        friendRequest.textContent = `${data.addressee}`
    } else if(data.addressee != myUsername && data.status === 'Accepted'){
        const friendListDiv = document.querySelector('#friends')
        const friendSpan = document.createElement('span')
        friendSpan.className = 'friend'
        friendSpan.textContent = `${data.addressee}`
        friendListDiv.appendChild(friendSpan)
        console.log('I am cool')
    } else if(data.addressee === myUsername && data.status === 'Accepted'){
        const friendListDiv = document.querySelector('#friends')
        const friendSpan = document.createElement('span')
        friendSpan.className = 'friend'
        friendSpan.textContent = `${data.requester}`
        friendListDiv.appendChild(friendSpan)
        console.log('I am cool')
    }
        
    friendRequestsList.appendChild(friendRequestContainer)
}

async function FriendRequest(addresseeUsername) {
    const response = await fetch(
        `/friendRequest?addresseeUsername=${encodeURIComponent(addresseeUsername)}`,
        {
            method: 'GET',
            headers: {
                'Authorization': 'Bearer ' + token
            }
        }
    )

    const data = await response.json()
    
    addFriend(data)
    console.log(friends)
}

async function GetFriendList() {
    const friendsListDiv = document.querySelector('#friends')
    const friendsRequestsDiv = document.querySelector('#friendRequests')
    friendsListDiv.innerHTML = ''
    friendsRequestsDiv.innerHTML = ''
    async function GetFriends() {
        var friendsList = await fetch(`/friendsList`, {
            method: 'GET',
            headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
        })
        return await friendsList.json()
    }
    var friendsHistory = await GetFriends()
    console.log(friendsHistory)
    friends.length = 0
    friendsHistory.forEach(element => {
        friends.push(element)
        addFriend(element)
    });
}

async function GetChatMembers(currentChatId) {
    const listsHolder = document.querySelector('#ListsHolder')
    listsHolder.innerHTML = `
    <div id="membersList">

    </div>`
    if(currentChatSelectionMode === 'ChatMembers'){
        async function GetMembers() {
            var membersList = await fetch(`/members?chat_id=${currentChatId}`, {
                method: 'GET',
                headers: {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
            })
            return await membersList.json()
        }
        const membersListHtml = document.querySelector('#membersList')
        const memberList = await GetMembers()
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
            membersListHtml.appendChild(row)
        }
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


document.querySelector('#messageText').addEventListener('keydown', (event) => {
    if(event.key === 'Enter'){
        Send()
    }
})
document.querySelector('#sendButton').addEventListener('click', (event)=>{ 
    const button = event.target.closest('#friendSendButton')
    if (!button) return
    Send()
})
document.querySelector('#ListsHolder').addEventListener('click', (event)=>{
    SendFriendRequest()
})
document.querySelector('#friendSendButton').addEventListener('keydown', (event)=>{
    if(event.key === 'Enter'){
        const button = event.target.closest('#friendSendButton')
        if (!button) return
        SendFriendRequest()
    }
})


connectWS()
ClickRegister()
AvailableChats()
GetFriendList()



