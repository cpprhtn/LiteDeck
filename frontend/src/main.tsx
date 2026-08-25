import React from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import { installWebTransport } from './webTransport'
import './tokens.css'
import './app.css'

// No-op under Wails; fills window.go / window.runtime when served over HTTP.
// Runs before any component calls a binding.
installWebTransport()

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
