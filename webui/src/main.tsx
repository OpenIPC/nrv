import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { ToastProvider } from './context/ToastContext'
// Подключается до отрисовки приложения, иначе первый кадр успел бы
// показать русские подписи, а следующий — уже переведённые, и экран
// мигал бы при каждом открытии.
import './i18n'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ToastProvider>
        <App />
      </ToastProvider>
    </BrowserRouter>
  </React.StrictMode>,
)