import React from 'react';
import { createRoot } from 'react-dom/client';
import { Projects } from './projects';
import { GitHubAdmin, managedGitHubRole } from './github-admin';
import { AdminOnboarding } from './admin-onboarding';
import './style.css';

const params = new URLSearchParams(location.search);
const github = location.pathname === '/admin/github';
const onboarding = location.pathname === '/admin' || github && (params.get('new_project') === '1' || managedGitHubRole(params));
createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {onboarding ? <AdminOnboarding /> : github
      ? <><div inert aria-hidden="true"><Projects behindGitHubAdmin /></div><GitHubAdmin /></>
      : <Projects />}
  </React.StrictMode>,
);
