import React from 'react';
import { createRoot } from 'react-dom/client';
import { Projects } from './projects';
import { GitHubAdmin } from './github-admin';
import './style.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode>{location.pathname === '/admin/github' ? <><div inert aria-hidden="true"><Projects behindGitHubAdmin /></div><GitHubAdmin /></> : <Projects />}</React.StrictMode>);
