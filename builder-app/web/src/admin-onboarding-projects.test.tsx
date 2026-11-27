import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { AdminOnboarding } from './admin-onboarding';

const directory = {
  companies: ['alpha', 'beta', 'gamma'].map(id => ({ id, name: 'Empresa ' + id, projects: [] })),
};
const project = { id: 'site', name: 'Site', company_id: 'alpha', company_name: 'Empresa alpha' };
const initialRoles = {
  project_name: 'Site',
  visual: { revision: 4, repository_id: 'team/shared', publication_branch: 'main' },
  implementation: { revision: 2, repositories: [{ repository_id: 'team/shared' }, { repository_id: 'team/backend' }] },
};
const prefix = '/admin/github/companies/alpha/projects/site';

function reply(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status }));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  history.replaceState(null, '', '/');
});

it('keeps global Projects available from Companies and labels projects by Company', async () => {
  const fetch = vi.fn((path: string) => path === '/admin/companies' ? reply(directory) : reply({ projects: [project] }));
  vi.stubGlobal('fetch', fetch);
  history.replaceState(null, '', '/admin');
  const companies = render(<AdminOnboarding />);
  expect(screen.getByRole('link', { name: /Projetos/ })).toHaveAttribute('href', '/admin?page=projects');
  companies.unmount();

  history.replaceState(null, '', '/admin?page=projects');
  render(<AdminOnboarding />);
  expect(await screen.findByRole('link', { name: /Site Empresa alpha/ })).toHaveAttribute('href', '/admin?page=projects&company_id=alpha&project_id=site');
  fireEvent.change(screen.getByRole('combobox', { name: 'Filtrar por empresa' }), { target: { value: 'alpha' } });
  fireEvent.change(screen.getByRole('textbox', { name: 'Buscar projetos' }), { target: { value: 'Site' } });
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/admin/projects?company_id=alpha&q=Site', expect.anything()));
  expect(fetch.mock.calls.every(([path]) => !path.startsWith(prefix))).toBe(true);
});

it('removes only the chosen role, prevents repeat submission and reads back the remaining Code links', async () => {
  history.replaceState(null, '', '/admin?page=projects&company_id=alpha&project_id=site');
  let finish: (response: Response) => void = () => undefined;
  const pending = new Promise<Response>(resolve => { finish = resolve; });
  const fetch = vi.fn((path: string) => {
    if (path === prefix) return reply(initialRoles);
    if (path === prefix + '/remove') return pending;
    return path === '/admin/companies' ? reply(directory) : reply({ projects: [project] });
  });
  vi.stubGlobal('fetch', fetch);
  render(<AdminOnboarding />);
  const details = screen.getByRole('region', { name: 'Detalhes do projeto' });
  const visualRemove = await within(details).findByRole('button', { name: 'Remover vínculo visual' });
  expect(within(details).getByRole('link', { name: 'Adicionar vínculo de código' })).toHaveAttribute('href', '/admin/github?company_id=alpha&project_id=site&managed_role=implementation');
  fireEvent.click(visualRemove);
  expect(within(details).getByText(/O Design System ficará sem vínculo/)).toBeInTheDocument();
  const confirm = within(details).getByRole('button', { name: 'Confirmar remoção' });
  fireEvent.click(confirm);
  fireEvent.click(confirm);
  expect(fetch.mock.calls.filter(([path]) => path.endsWith('/remove'))).toHaveLength(1);
  expect(fetch).toHaveBeenCalledWith(prefix + '/remove', expect.objectContaining({
    method: 'POST',
    body: expect.stringMatching(/"role":"visual","repository_id":"team\/shared","expected_revision":4/),
  }));
  finish(new Response(JSON.stringify({ ...initialRoles, visual: { revision: 5 } })));
  expect(await within(details).findByText('Vínculo removido.')).toBeInTheDocument();
  expect(within(details).queryByRole('button', { name: 'Remover vínculo visual' })).toBeNull();
  expect(within(details).getByRole('button', { name: 'Remover vínculo de código team/shared' })).toBeInTheDocument();
});

it('reports permission denial without offering mutations', async () => {
  history.replaceState(null, '', '/admin?page=projects&company_id=alpha&project_id=site');
  vi.stubGlobal('fetch', (path: string) => {
    if (path === prefix) return reply({}, 403);
    return path === '/admin/companies' ? reply(directory) : reply({ projects: [project] });
  });
  render(<AdminOnboarding />);
  expect(await screen.findByText('Você não tem permissão para editar os vínculos deste projeto.')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /remover vínculo/i })).toBeNull();
  expect(screen.queryByRole('link', { name: /adicionar vínculo/i })).toBeNull();
});
