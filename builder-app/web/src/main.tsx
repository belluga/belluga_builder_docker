import React from 'react';
import { createRoot } from 'react-dom/client';
import { Projects } from './projects';
import { GitHubAdmin } from './github-admin';
import { AdminOnboarding } from './admin-onboarding';
import './style.css';

createRoot(document.getElementById('root')!).render(<React.StrictMode>{location.pathname === '/admin' || location.pathname === '/admin/github' && new URLSearchParams(location.search).get('new_project') === '1' ? <AdminOnboarding /> : location.pathname === '/admin/github' ? <><div inert aria-hidden="true"><Projects behindGitHubAdmin /></div><GitHubAdmin /></> : <Projects />}</React.StrictMode>);
