import React from 'react';
import { createRoot } from 'react-dom/client';
import { Viewer } from './viewer';
import { Projects } from './projects';
import './style.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode>{location.pathname === '/local' ? <Projects /> : <Viewer />}</React.StrictMode>);
