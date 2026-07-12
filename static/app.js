function RegisterHandler(){
    const RegisterButton = document.querySelector('#RegisterButton')
    const username = document.querySelector('#username')
    const password = document.querySelector('#password')
    RegisterButton.addEventListener('click', async (event) => {
        const RegisterResponse = await fetch('/register', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({'username':username.value, 'password':password.value})
        })
        console.log(RegisterResponse)
    })
}
function LoginHandler(){
    const LoginButton = document.querySelector('#LoginButton')
    const username = document.querySelector('#username')
    const password = document.querySelector('#password')
    var token = ""
    LoginButton.addEventListener('click', async (event) => {
        const LoginResponse = await fetch('/login', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({'username':username.value, 'password':password.value})
        })
        token = await LoginResponse.text()
        localStorage.setItem('token', token)
        window.location.href = '/chat.html'
    })
}
RegisterHandler()
LoginHandler()