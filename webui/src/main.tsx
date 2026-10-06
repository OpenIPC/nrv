import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { ToastProvider } from './context/ToastContext'
import { PermissionsProvider } from './context/PermissionsContext'
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
        {/* Права выше приложения: меню и страницы читают их при первой
            отрисовке, иначе на миг показались бы все разделы. */}
        <PermissionsProvider>
          <App />
        </PermissionsProvider>
      </ToastProvider>
    </BrowserRouter>
  </React.StrictMode>,
)