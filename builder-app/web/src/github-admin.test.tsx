import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GitHubAdmin } from './github-admin';
import { Projects } from './projects';

const entry = '/admin/github?company_id=alpha&project_id=site';
const prefix = '/admin/github/companies/alpha/projects/site';
const roles = { visual: { revision: 7, publication_branch: 'main' }, implementation: { revision: 2, repositories: [] } };
function reply(value: unknown) {
  return new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  sessionStorage.clear();
  history.replaceState(null, '', '/');
});

describe('GitHub administration', () => {
  it('offers the entry from a Project after the Registry authorizes its operator', async () => {
    history.replaceState(null, '', '/?company_id=alpha&project_id=site');
    const request = vi.fn(async (path: string) => {
      if (path === '/api/catalog') return reply({
        companies: [{ id: 'alpha', name: 'Alpha' }],
        projects: [{ id: 'site', company_id: 'alpha', name: 'Site' }],
      });
      if (path === prefix) return reply(roles);
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal('fetch', request);
    const view = render(<Projects />);
    expect(await screen.findByRole('link', { name: 'Gerenciar conexão GitHub' }, { timeout: 5000 })).toHaveAttribute('href', entry);
    view.unmount();

    request.mockImplementation(async path => {
      if (path === '/api/catalog') return reply({
        companies: [{ id: 'alpha', name: 'Alpha' }],
        projects: [{ id: 'site', company_id: 'alpha', name: 'Site' }],
      });
      return new Response(null, { status: 403 });
    });
    request.mockClear();
    render(<Projects />);
    await waitFor(() => expect(request).toHaveBeenCalledWith(prefix, expect.anything()));
    expect(screen.queryByRole('link', { name: 'Gerenciar conexão GitHub' })).not.toBeInTheDocument();
  }, 10000);

  it('uses authoritative role revision and saves only the intent before navigating', async () => {
    history.replaceState(null, '', entry);
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      const intent = JSON.parse(String(init?.body));
      expect(intent.expected_role_revision).toBe(7);
      expect(intent.expected_connection_revision).toBe(0);
      expect(intent.target_account_id).toBe(101);
      expect(intent.publication_branch).toBe('main');
      return reply({ state: 'pending', operation_id: intent.operation_id });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    const account = await screen.findByLabelText('ID numérico da conta ou organização');
    fireEvent.change(account, { target: { value: '101' } });
    fireEvent.submit(account.closest('form')!);
    await screen.findByText('Aguardando autorização no GitHub');
    expect(request.mock.calls[1][0]).toBe(prefix + '/operations');
    const saved = JSON.parse(sessionStorage.getItem('builder-github:alpha:site')!);
    expect(Object.keys(saved)).toEqual(['intent']);
    expect(saved.intent.role).toBe('visual');
  });

  it('resumes the original operation after the callback and submits the selected numeric repo', async () => {
    history.replaceState(null, '', entry + '&operation_id=github-one');
    sessionStorage.setItem('builder-github:alpha:site', JSON.stringify({ intent: {
      operation_id: 'github-one', role: 'implementation', action: 'connect', expected_role_revision: 2,
      expected_connection_revision: 0, target_account_id: 101,
    } }));
    const visible: Array<() => void> = [];
    vi.stubGlobal('IntersectionObserver', class {
      constructor(private notify: IntersectionObserverCallback) {}
      observe() {
        visible.push(() => this.notify([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver));
      }
      disconnect() {}
    });
    let finishSecondPage!: (value: Response) => void;
    const secondPage = new Promise<Response>(resolve => { finishSecondPage = resolve; });
    let pageTwoAttempts = 0;
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path.endsWith('?page=1')) return reply({ repositories: [{ github_repository_id: 456, repository_full_name: 'alpha/one', default_branch: 'main' }], has_next: true });
      if (path.endsWith('?page=2')) {
        if (++pageTwoAttempts === 1) return new Response('{"teach":"Lista temporariamente indisponível."}', { status: 503 });
        return secondPage;
      }
      if (path.endsWith('/finalize')) {
        expect(JSON.parse(String(init?.body))).toEqual({ expected_role_revision: 2, expected_connection_revision: 0, github_repository_id: 457 });
        return reply({ state: 'completed', operation_id: 'github-one' });
      }
      return reply({ state: 'verified', operation_id: 'github-one' });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    const search = await screen.findByLabelText('Buscar repositórios');
    fireEvent.change(search, { target: { value: 'two' } });
    await act(async () => visible[0]());
    expect(screen.queryByText('Nenhum repositório nesta busca.')).not.toBeInTheDocument();
    await act(async () => { visible[1](); visible[1](); });
    expect(screen.getByRole('alert')).toHaveTextContent('Lista temporariamente indisponível.');
    act(() => { visible[1](); });
    expect(pageTwoAttempts).toBe(1);
    fireEvent.click(screen.getByText('Tentar carregar repositórios novamente'));
    expect(screen.getByText('Carregando repositórios…')).toBeInTheDocument();
    expect(pageTwoAttempts).toBe(2);
    finishSecondPage(reply({ repositories: [{ github_repository_id: 457, repository_full_name: 'alpha/two', default_branch: 'main' }], has_next: false }));
    fireEvent.click(await screen.findByText('alpha/two'));
    fireEvent.click(screen.getByText('Confirmar vínculo'));
    await waitFor(() => expect(screen.getByText('Repositório vinculado')).toBeInTheDocument());
  });
});
