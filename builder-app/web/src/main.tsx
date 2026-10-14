import React from 'react';
import { createRoot } from 'react-dom/client';
import { Projects } from './projects';
import './style.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode><Projects /></React.StrictMode>);
