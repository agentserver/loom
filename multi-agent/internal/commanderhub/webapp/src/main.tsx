import React from 'react';
import { createRoot } from 'react-dom/client';
import { CommanderApp } from './CommanderApp';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <CommanderApp />
  </React.StrictMode>,
);
