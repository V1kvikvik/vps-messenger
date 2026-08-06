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
        if (!RegisterResponse.ok) {
            alert('Регистрация не удалась. Проверьте имя пользователя и пароль.')
            return
        }

        const LoginResponse = await fetch('/login', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({'username':username.value, 'password':password.value})
        })
        if (!LoginResponse.ok) {
            alert('Регистрация прошла, но авто-вход не удался. Попробуйте войти вручную.')
            return
        }

        const token = await LoginResponse.text()
        localStorage.setItem('token', token)
        window.location.href = '/chat.html'
    })
}

function LoginHandler(){
    const LoginButton = document.querySelector('#LoginButton')
    const username = document.querySelector('#username')
    const password = document.querySelector('#password')
    LoginButton.addEventListener('click', async (event) => {
        const LoginResponse = await fetch('/login', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({'username':username.value, 'password':password.value})
        })
        if (!LoginResponse.ok) {
            alert('Неверное имя пользователя или пароль.')
            return
        }
        const token = await LoginResponse.text()
        localStorage.setItem('token', token)
        window.location.href = '/chat.html'
    })
}

RegisterHandler()
LoginHandler()