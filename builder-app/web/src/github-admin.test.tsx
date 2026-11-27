import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { GitHubAdmin } from './github-admin';
import { Projects } from './projects';
import { AdminOnboarding } from './admin-onboarding';

const entry = '/admin/github?company_id=alpha&project_id=site';
const prefix = '/admin/github/companies/alpha/projects/site';
const roles = { visual: { revision: 7, publication_branch: 'main' }, implementation: { revision: 2, repositories: [] } };
function reply(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
}

function stubFetch(request: (path: string, init?: RequestInit) => Promise<Response>) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    if (typeof init?.body === 'string' && JSON.parse(init.body).action === 'resume-discovery') {
      return Promise.resolve(reply({ state: 'pending' }));
    }
    if (path.endsWith('/pending-installation') || path.endsWith('/github-pending-installation')) return Promise.resolve(reply({ operation: null }));
    return request(path, init);
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  sessionStorage.clear();
  history.replaceState(null, '', '/');
});

describe('GitHub administration', () => {
  it('opens verified installation settings separately and refreshes the selected account without losing the search', async () => {
    history.replaceState(null, '', entry);
    const personalSettings = 'https://github.com/settings/installations/1';
    const orgSettings = 'https://github.com/organizations/requested/settings/installations/2';
    const installations = [
      { installation_id: 1, type: 'User', login: 'personal', settings_url: personalSettings },
      { installation_id: 2, type: 'Organization', login: 'requested', settings_url: orgSettings },
      { installation_id: 3, type: 'Organization', login: 'unrelated', settings_url: 'https://github.com.evil.example/settings/installations/3' },
    ];
    vi.stubGlobal('IntersectionObserver', class {
      constructor(private notify: IntersectionObserverCallback) {}
      observe() {
        queueMicrotask(() => this.notify([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver));
      }
      disconnect() {}
    });
    let load = 0;
    let operationID = '';
    let lateSignal: AbortSignal | null | undefined;
    let overlappingSignal: AbortSignal | null | undefined;
    let switchedSignal: AbortSignal | null | undefined;
    let returnLate: (value: Response) => void = () => {};
    let returnOverlapping: (value: Response) => void = () => {};
    let returnSwitched: (value: Response) => void = () => {};
    const late = new Promise<Response>(resolve => { returnLate = resolve; });
    const overlapping = new Promise<Response>(resolve => { returnOverlapping = resolve; });
    const switched = new Promise<Response>(resolve => { returnSwitched = resolve; });
    const repo = (id: number, name: string) => ({ github_repository_id: id, repository_full_name: name, default_branch: 'main' });
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      const repositoriesPath = prefix + '/operations/' + operationID + '/repositories?page=';
      if (path === repositoriesPath + '1') {
        load++;
        if (load === 1) return reply({ repositories: [repo(10, 'requested/old')], has_next: true });
        if (load === 3) {
          overlappingSignal = init?.signal;
          return overlapping;
        }
        if (load === 4) return reply({ teach: 'GitHub temporariamente indisponível.' }, 503);
        if (load === 5) return reply({ error: 'github_user_authorization_required' }, 409);
        if (load === 6) {
          switchedSignal = init?.signal;
          return switched;
        }
        if (load === 7) return reply({ repositories: [repo(20, 'personal/available')], has_next: false });
        return reply({ repositories: [repo(11, 'requested/new')], has_next: false });
      }
      if (path === repositoriesPath + '2') {
        lateSignal = init?.signal;
        return late;
      }
      expect(path).toBe(prefix + '/operations');
      const form = JSON.parse(String(init?.body));
      if (form.action === 'discover') {
        if (operationID) expect(form.operation_id).not.toBe(operationID);
        operationID = form.operation_id;
        return reply({ state: 'pending', operation_id: operationID, installations });
      }
      expect(form).toMatchObject({ action: 'select-installation', operation_id: operationID, installation_id: load === 6 ? 1 : 2 });
      const selected = installations.find(installation => installation.installation_id === form.installation_id)!;
      return reply({ state: 'verified', operation_id: operationID, github_account_login: selected.login, github_account_type: selected.type, settings_url: selected.settings_url });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Conectar GitHub' }));
    const links = await screen.findAllByRole('link', { name: 'Configurar acesso' });
    expect(links.map(link => link.getAttribute('href'))).toEqual([personalSettings, orgSettings]);
    expect(links[0]).toHaveAttribute('target', '_blank');
    expect(links[0]).toHaveAttribute('rel', 'noopener noreferrer');
    expect(request.mock.calls.filter(([, init]) => String(init?.body).includes('select-installation'))).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: /@requested.*Organização/ }));
    expect(await screen.findByRole('link', { name: 'Configurar acesso' })).toHaveAttribute('href', orgSettings);
    fireEvent.click(await screen.findByRole('radio', { name: /Selecionar repositório existente/ }));
    fireEvent.click(await screen.findByRole('radio', { name: /requested\/old/ }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Buscar repositórios' }), { target: { value: 'requested' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Papel do repositório' }), { target: { value: 'implementation' } });
    await waitFor(() => expect(lateSignal).toBeDefined());
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar repositórios' }));
    expect(lateSignal?.aborted).toBe(true);
    expect(screen.getByRole('button', { name: 'Confirmar vínculo' })).toBeDisabled();
    expect(await screen.findByRole('radio', { name: /requested\/new/ })).not.toBeChecked();
    await act(async () => { returnLate(reply({ repositories: [repo(12, 'requested/stale')], has_next: false })); });
    expect(screen.queryByRole('radio', { name: /requested\/(old|stale)/ })).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Buscar repositórios' })).toHaveValue('requested');
    expect(screen.getByRole('combobox', { name: 'Papel do repositório' })).toHaveValue('implementation');
    expect(load).toBe(2);
    fireEvent.click(screen.getByRole('radio', { name: /requested\/new/ }));
    const refresh = screen.getByRole('button', { name: 'Atualizar repositórios' });
    fireEvent.click(refresh);
    expect(overlappingSignal).toBeDefined();
    fireEvent.click(refresh);
    expect(overlappingSignal?.aborted).toBe(true);
    expect(await screen.findByText('GitHub temporariamente indisponível.')).toHaveAttribute('role', 'alert');
    await act(async () => { returnOverlapping(reply({ repositories: [repo(13, 'requested/obsolete')], has_next: false })); });
    expect(screen.queryByRole('radio', { name: /requested\/(new|obsolete)/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Confirmar vínculo' })).toBeDisabled();

    fireEvent.click(refresh);
    expect(await screen.findByRole('button', { name: 'Retomar autorização' })).toBeInTheDocument();
    fireEvent.click(refresh);
    expect(switchedSignal).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Trocar conta' }));
    expect(switchedSignal?.aborted).toBe(true);
    fireEvent.click(await screen.findByRole('button', { name: /@personal.*Conta pessoal/ }));
    expect(await screen.findByRole('link', { name: 'Configurar acesso' })).toHaveAttribute('href', personalSettings);
    fireEvent.click(screen.getByRole('radio', { name: /Selecionar repositório existente/ }));
    expect(await screen.findByRole('radio', { name: /personal\/available/ })).not.toBeChecked();
    await act(async () => { returnSwitched(reply({ repositories: [repo(14, 'requested/previous-account')], has_next: false })); });
    expect(screen.queryByRole('radio', { name: /requested\// })).not.toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /personal\/available/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Confirmar vínculo' })).toBeDisabled();
  });

  it.each([false, true])('restores an organization request and refreshes with a fresh discovery (callback: %s)', async fromCallback => {
    const companyEntry = '/admin/github?company_id=alpha&new_project=1';
    const discoveryPath = '/admin/companies/alpha/github-discovery';
    const receipt = { operation_id: 'github-original', state: 'awaiting_organization_approval', installation_requested_at: '2026-11-26T12:00:00Z', expires_at: '2026-12-26T12:00:00Z' };
    history.replaceState(null, '', companyEntry + (fromCallback ? '&operation_id=github-original' : ''));
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/admin/companies/alpha/github-pending-installation') return reply({ operation: receipt });
      if (path === discoveryPath + '/github-original') return reply(receipt);
      if (path === discoveryPath && typeof init?.body === 'string') {
        const body = JSON.parse(init.body);
        if (body.action === 'discover') {
          expect(body.operation_id).not.toBe(receipt.operation_id);
          return reply({ operation_id: body.operation_id, state: 'pending', installations: [{ installation_id: 1, type: 'User', login: 'personal' }] });
        }
        return reply({ state: 'pending' });
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    expect(await screen.findByText('Aguardando aprovação da organização')).toBeInTheDocument();
    if (fromCallback) expect(request).toHaveBeenCalledWith(discoveryPath + '/github-original', undefined);
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar instalações' }));
    expect(await screen.findByText('@personal')).toBeInTheDocument();
    expect(screen.getByText('Aguardando aprovação da organização')).toBeInTheDocument();
    expect(screen.getByText(/Se a organização solicitada não aparecer/)).toBeInTheDocument();
  });

  it.each([false, true])('keeps the receipt while selecting an organization (eligible repository: %s)', async eligible => {
    history.replaceState(null, '', entry);
    const receipt = { operation_id: 'github-original', state: 'awaiting_organization_approval' };
    const repository = { github_repository_id: 5, repository_full_name: 'requested/app', default_branch: 'main' };
    const installations = [
      { installation_id: 1, type: 'User', login: 'personal' },
      { installation_id: 2, type: 'Organization', login: 'requested' },
      { installation_id: 3, type: 'Organization', login: 'unrelated' },
    ];
    vi.stubGlobal('IntersectionObserver', class {
      constructor(private notify: IntersectionObserverCallback) {}
      observe() {
        queueMicrotask(() => this.notify([{ isIntersecting: true } as IntersectionObserverEntry], this as unknown as IntersectionObserver));
      }
      disconnect() {}
    });
    let discovery = '';
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/pending-installation') return reply({ operation: receipt });
      if (path.endsWith('/repositories?page=1')) {
        expect(path).toBe(prefix + '/operations/' + discovery + '/repositories?page=1');
        return reply({ repositories: eligible ? [repository] : [], has_next: false });
      }
      expect(path).toBe(prefix + '/operations');
      const body = JSON.parse(String(init?.body));
      if (body.action === 'resume-discovery') return reply({ state: 'pending' });
      if (body.action === 'discover') {
        discovery = body.operation_id;
        expect(discovery).not.toBe(receipt.operation_id);
        return reply({ operation_id: discovery, state: 'pending', installations });
      }
      expect(body).toEqual({ action: 'select-installation', operation_id: discovery, installation_id: 2 });
      return reply({ operation_id: discovery, state: 'verified', github_account_login: 'requested', github_account_type: 'Organization' });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Atualizar instalações' }));
    expect(await screen.findByRole('button', { name: /@requested.*Organização/ })).toBeEnabled();
    expect(screen.getByRole('button', { name: /@personal.*Conta pessoal/ })).toBeEnabled();
    expect(screen.getByRole('button', { name: /@unrelated.*Organização/ })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: /@requested.*Organização/ }));
    fireEvent.click(await screen.findByRole('radio', { name: /Selecionar repositório existente/ }));
    const save = screen.getByRole('button', { name: 'Confirmar vínculo' });
    expect(save).toBeDisabled();
    if (eligible) {
      fireEvent.click(await screen.findByRole('radio', { name: /requested\/app/ }));
      expect(save).toBeEnabled();
    } else {
      expect(await screen.findByText('Nenhum repositório nesta busca.')).toBeInTheDocument();
      expect(save).toBeDisabled();
    }
    expect(screen.getByText('Aguardando aprovação da organização')).toBeInTheDocument();
  });

  it('does not restart discovery while a separate binding is active', async () => {
    history.replaceState(null, '', entry + '&operation_id=github-binding');
    const receipt = { operation_id: 'github-original', state: 'awaiting_organization_approval' };
    vi.stubGlobal('fetch', async (path: string) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/pending-installation') return reply({ operation: receipt });
      if (path === prefix + '/operations/github-binding') return reply({ state: 'pending', operation_id: 'github-binding' });
      return new Response(null, { status: 404 });
    });
    render(<GitHubAdmin />);
    expect(await screen.findByText('Aguardando aprovação da organização')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Atualizar instalações' })).not.toBeInTheDocument();
  });

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
    stubFetch(request);
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

  it('starts with a GitHub connection action before requesting repository details', async () => {
    history.replaceState(null, '', entry);
    vi.stubGlobal('fetch', vi.fn(async () => reply(roles)));
    render(<GitHubAdmin />);

    expect(await screen.findByRole('button', { name: 'Conectar GitHub' })).toBeEnabled();
    expect(screen.queryByLabelText('ID numérico da conta ou organização')).not.toBeInTheDocument();
    expect(screen.queryByText('Selecionar repositório existente')).not.toBeInTheDocument();
    expect(screen.queryByText('Criar novo repositório')).not.toBeInTheDocument();
  });

  it.each([false, true])('opens the authorized picker automatically and puts Create first (new Project: %s)', async newProject => {
    const startPath = newProject ? '/admin/companies/alpha/github-discovery' : prefix + '/operations';
    history.replaceState(null, '', newProject ? '/admin/github?company_id=alpha&new_project=1' : entry);
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      expect(path).toBe(startPath);
      const input = JSON.parse(String(init?.body));
      if (input.action === 'resume-discovery') return reply({ state: 'pending', operation_id: input.operation_id, installations: [
        { installation_id: 7, login: 'belluga', type: 'Organization' },
      ] });
      expect(input.action).toBe('select-installation');
      return reply({ state: 'verified', operation_id: input.operation_id, github_account_login: 'belluga', github_account_type: 'Organization' });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: /@belluga.*Organização/ }));
    const create = await screen.findByRole('radio', { name: /Criar novo repositório/ });
    const select = screen.getByRole('radio', { name: /Selecionar repositório existente/ });
    expect(create.compareDocumentPosition(select) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(create).not.toBeChecked();
    expect(select).not.toBeChecked();
    fireEvent.click(create);
    expect(screen.getByLabelText('Nome do repositório').compareDocumentPosition(select) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('shows a passive Registry failure and keeps manual GitHub connection available', async () => {
    history.replaceState(null, '', entry);
    let explicit = false;
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      const input = JSON.parse(String(init?.body));
      if (input.action === 'resume-discovery') return new Response(JSON.stringify({ teach: 'O Registry está indisponível.' }), { status: 503 });
      expect(input.action).toBe('discover');
      explicit = true;
      return reply({ state: 'pending', operation_id: input.operation_id });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    expect(await screen.findByRole('alert')).toHaveTextContent('O Registry está indisponível.');
    const button = screen.getByRole('button', { name: 'Conectar GitHub' });
    expect(button).toBeEnabled();
    expect(explicit).toBe(false);
    fireEvent.click(button);
    await waitFor(() => expect(explicit).toBe(true));
  });

  it.each([false, true])('offers explicit reconnection for a stale saved discovery (expired: %s)', async expired => {
    history.replaceState(null, '', entry + '&operation_id=old-discovery');
    sessionStorage.setItem('builder-github:alpha:site:discovery', 'old-discovery');
    let explicitID = '';
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/operations/old-discovery') return reply({
        state: 'pending', operation_id: 'old-discovery', discovery_expired: expired,
      });
      expect(path).toBe(prefix + '/operations');
      const input = JSON.parse(String(init?.body));
      if (input.action === 'resume-discovery') return reply({ state: 'pending' });
      expect(input.action).toBe('discover');
      explicitID = input.operation_id;
      return reply({ state: 'pending', operation_id: explicitID, installations: [] });
    });
    vi.stubGlobal('fetch', request);
    render(<GitHubAdmin />);
    const connect = await screen.findByRole('button', { name: 'Conectar GitHub' });
    expect(screen.queryByText('GitHub conectado')).not.toBeInTheDocument();
    fireEvent.click(connect);
    await screen.findByText('Escolha uma conta ou organização do GitHub');
    if (expired) expect(explicitID).not.toBe('old-discovery');
    else expect(explicitID).toBe('old-discovery');
  });

  it('starts a fresh discovery when a setup link is already open', async () => {
    history.replaceState(null, '', entry + '&operation_id=picker');
    sessionStorage.setItem('builder-github:alpha:site:discovery', 'picker');
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/operations/picker') return reply({ state: 'pending', operation_id: 'picker', installations: [] });
      expect(path).toBe(prefix + '/operations');
      const input = JSON.parse(String(init?.body));
      if (input.action === 'install-app') {
        expect(input.operation_id).toBe('picker');
        return new Response(JSON.stringify({ teach: 'O link de instalação já está aberto.' }), { status: 409 });
      }
      expect(input.action).toBe('discover');
      expect(input.operation_id).not.toBe('picker');
      return reply({ state: 'pending', operation_id: input.operation_id, installations: [] });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Instalar o GitHub App em outra conta' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('O link de instalação já está aberto.');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar contas disponíveis' }));
    await waitFor(() => expect(request.mock.calls.some(([, init]) => typeof init?.body === 'string' && JSON.parse(init.body).action === 'discover')).toBe(true));
  });

  it('chooses and changes an authorized account in Builder before showing repository actions', async () => {
    history.replaceState(null, '', entry + '&operation_id=picker');
    sessionStorage.setItem('builder-github:alpha:site:discovery', 'picker');
    const installations = [
      { installation_id: 7, login: 'belluga', type: 'Organization' },
      { installation_id: 8, login: 'elton', type: 'User' },
    ];
    let nextOperation = '';
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/operations/picker') return reply({ state: 'pending', operation_id: 'picker', installations });
      expect(path).toBe(prefix + '/operations');
      const input = JSON.parse(String(init?.body));
      if (input.action === 'discover') {
        expect(input.operation_id).not.toBe('picker');
        nextOperation = input.operation_id;
        return reply({ state: 'pending', operation_id: nextOperation, installations });
      }
      const choice = installations[nextOperation ? 1 : 0];
      expect(input).toEqual({ action: 'select-installation', operation_id: nextOperation || 'picker', installation_id: choice.installation_id });
      return reply({ state: 'verified', operation_id: input.operation_id, github_account_login: choice.login, github_account_type: choice.type });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    expect(await screen.findByText('Escolha uma conta ou organização do GitHub')).toBeInTheDocument();
    expect(screen.queryByText('Selecionar repositório existente')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /@belluga.*Organização/ }));
    expect(await screen.findByText('Selecionar repositório existente')).toBeInTheDocument();
    expect(screen.getByText('@belluga')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Trocar conta' }));
    fireEvent.click(await screen.findByRole('button', { name: /@elton.*Conta pessoal/ }));
    expect(await screen.findByText('GitHub conectado')).toBeInTheDocument();
    expect(screen.getByText('@elton')).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Criar novo repositório/ })).toBeDisabled();
  });

  it('offers explicit installation when GitHub returns no authorized account', async () => {
    history.replaceState(null, '', entry);
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      expect(path).toBe(prefix + '/operations');
      const input = JSON.parse(String(init?.body));
      return reply({ state: 'pending', operation_id: input.operation_id, installations: [] });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Conectar GitHub' }));
    await screen.findByText('Nenhuma instalação do GitHub App está disponível para esta conta.');
    fireEvent.click(screen.getByRole('button', { name: 'Instalar o GitHub App em outra conta' }));
    await waitFor(() => expect(request.mock.calls.some(([, init]) => init?.body && JSON.parse(String(init.body)).action === 'install-app')).toBe(true));
  });

  it('validates a repository name before writing the Project or binding and keeps the draft if the Project ID is missing', async () => {
    const newProject = '/admin/github?company_id=alpha&new_project=1';
    const discovery = '/admin/companies/alpha/github-discovery';
    const registration = '/admin/companies/alpha/projects';
    const binding = '/admin/github/companies/alpha/projects/teste-efemero/operations';
    history.replaceState(null, '', newProject);
    const registrationIntents: object[] = [];
    let bindingIntent: object | undefined;
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      const input = JSON.parse(String(init?.body));
      if (path === discovery) return reply({ state: 'verified', operation_id: input.operation_id, github_account_login: 'alpha', github_account_type: 'Organization' });
      if (path === registration) {
        registrationIntents.push(input);
        return reply(registrationIntents.length === 1 ? {} : { id: 'teste-efemero' });
      }
      if (path === binding) {
        bindingIntent = input;
        return reply({ state: 'verified', operation_id: input.operation_id });
      }
      if (path.endsWith('/finalize')) return reply({ state: 'completed', operation_id: path.split('/').at(-2), project_id: 'teste-efemero' });
      throw Error('Unexpected request ' + path);
    });
    stubFetch(request);
    render(<GitHubAdmin companyName="Alpha" />);
    fireEvent.change(screen.getByLabelText(/Nome do projeto/), { target: { value: 'Teste efêmero' } });
    fireEvent.click(await screen.findByRole('button', { name: 'Conectar GitHub' }));
    fireEvent.click(await screen.findByText('Criar novo repositório'));
    const repository = screen.getByLabelText('Nome do repositório');
    fireEvent.click(screen.getByText('Público'));
    for (const invalid of ['teste efemero', ' teste-efemero', 'time/repo', '.repo', 'repo.', 'repo..name', 'a'.repeat(101)]) {
      fireEvent.change(repository, { target: { value: invalid } });
      expect(repository).toHaveAttribute('aria-invalid', 'true');
      expect(screen.getByRole('button', { name: 'Salvar' })).toBeDisabled();
    }
    expect(request.mock.calls.map(([path]) => path)).toEqual([discovery]);
    expect(location.pathname + location.search).toBe(newProject);
    fireEvent.change(repository, { target: { value: 'Teste_Efemero.v1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Salvar' }));
    expect(await screen.findByText(/ainda não confirmou o ID do projeto/)).toBeInTheDocument();
    expect(location.pathname + location.search).toBe(newProject);
    expect(screen.getByLabelText(/Nome do projeto/)).toHaveValue('Teste efêmero');
    expect(repository).toHaveValue('Teste_Efemero.v1');
    expect(request.mock.calls.map(([path]) => path)).toEqual([discovery, registration]);
    fireEvent.click(screen.getByRole('button', { name: 'Salvar' }));
    await waitFor(() => expect(request.mock.calls.some(([path]) => path.startsWith(binding + '/') && path.endsWith('/finalize'))).toBe(true));
    await waitFor(() => expect(sessionStorage.getItem('builder-onboarding-project:alpha:binding')).toBeNull());
    expect(registrationIntents).toHaveLength(2);
    expect(registrationIntents[0]).toEqual(registrationIntents[1]);
    expect(bindingIntent).toMatchObject({ action: 'create', target_repository_name: 'Teste_Efemero.v1', visibility: 'public' });
    expect(new URLSearchParams(location.search).get('project_id')).toBe('teste-efemero');
  });

  it('opens normal entry instead of retrying a stale invalid stored repository intent', async () => {
    history.replaceState(null, '', entry);
    sessionStorage.setItem('builder-github:alpha:site', JSON.stringify({ intent: {
      operation_id: 'invalid-name', role: 'visual', action: 'create', target_repository_name: 'teste efemero',
    } }));
    const request = vi.fn(async (path: string) => {
      expect(path).toBe(prefix);
      return reply(roles);
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    expect(await screen.findByRole('button', { name: 'Conectar GitHub' })).toBeEnabled();
    expect(request).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: 'Tentar novamente' })).not.toBeInTheDocument();
  });

  it('discovers the verified account before submitting a typed public creation intent', async () => {
    history.replaceState(null, '', entry);
    const captured: object[] = [];
    let discoveryID = '';
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      const input = JSON.parse(String(init?.body));
      captured.push(input);
      expect(init?.method).toBe('POST');
      if (input.action === 'discover') {
        expect(path).toBe(prefix + '/operations');
        discoveryID = input.operation_id;
        return reply({ state: 'verified', operation_id: discoveryID, github_account_login: 'alpha', github_account_type: 'Organization' });
      }
      if (path.endsWith('/finalize')) {
        expect(path).toBe(prefix + '/operations/' + discoveryID + '-binding/finalize');
        expect(input).toEqual({ expected_role_revision: 7, expected_connection_revision: 0 });
        return reply({ state: 'completed', operation_id: discoveryID + '-binding', repository: { github_repository_id: 44, repository_full_name: 'alpha/approved', default_branch: 'trunk' } });
      }
      expect(path).toBe(prefix + '/operations');
      expect(input.operation_id).toBe(discoveryID + '-binding');
      return reply({ state: 'verified', operation_id: input.operation_id });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Conectar GitHub' }));
    expect(await screen.findByText('@alpha')).toBeInTheDocument();
    expect(captured[0]).toEqual({ action: 'discover', operation_id: expect.any(String) });
    fireEvent.click(screen.getByText('Criar novo repositório'));
    fireEvent.change(screen.getByLabelText('Nome do repositório'), { target: { value: 'approved' } });
    fireEvent.click(screen.getByText('Público'));
    fireEvent.click(screen.getByRole('button', { name: 'Criar repositório' }));
    await screen.findByText('Repositório vinculado');
    expect(screen.getByText('alpha/approved')).toBeInTheDocument();
    expect(captured[1]).toMatchObject({ role: 'visual', action: 'create', expected_role_revision: 7, expected_connection_revision: 0, discovery_operation_id: expect.any(String), target_repository_name: 'approved', visibility: 'public' });
    expect(captured[1]).not.toHaveProperty('target_account_id');
  });

  it('recovers a persisted failed operation after GitHub denies the requested visibility', async () => {
    history.replaceState(null, '', entry);
    let operationID = '';
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === prefix) return reply(roles);
      if (path === prefix + '/operations/' + operationID) {
        expect(init?.method).toBeUndefined();
        return reply({ state: 'failed', operation_id: operationID });
      }
      if (path.endsWith('/finalize')) {
        expect(path).toBe(prefix + '/operations/' + operationID + '/finalize');
        return new Response('{"teach":"Política do GitHub recusou a visibilidade."}', { status: 403 });
      }
      const input = JSON.parse(String(init?.body));
      if (input.action === 'discover') return reply({ state: 'verified', operation_id: input.operation_id, github_account_login: 'alpha', github_account_type: 'Organization' });
      operationID = input.operation_id;
      return reply({ state: 'verified', operation_id: operationID });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Conectar GitHub' }));
    fireEvent.click(await screen.findByText('Criar novo repositório'));
    fireEvent.change(screen.getByLabelText('Nome do repositório'), { target: { value: 'policy-denied' } });
    fireEvent.click(screen.getByText('Público'));
    fireEvent.click(screen.getByRole('button', { name: 'Criar repositório' }));
    expect(await screen.findByText('Política do GitHub recusou a visibilidade.')).toBeInTheDocument();
    expect(screen.getByText('Operação encerrada')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Nova conexão' })).toBeEnabled();
    expect(screen.queryByRole('button', { name: 'Criar repositório' })).not.toBeInTheDocument();
  });

  it('settles a recovered completed Project and releases the registration draft', async () => {
    const operation = 'created-project-binding';
    const creation = 'builder-onboarding-project:alpha';
    history.replaceState(null, '', entry + '&operation_id=' + operation);
    sessionStorage.setItem(creation, JSON.stringify({ name: 'Original', slug: 'site', operation: 'registration-original' }));
    sessionStorage.setItem(creation + ':binding', operation);
    sessionStorage.setItem('builder-github:alpha::discovery', 'original-discovery');
    const request = vi.fn(async (path: string) => path === prefix
      ? reply(roles)
      : reply({ state: 'completed', operation_id: operation, project_id: 'site' }));
    stubFetch(request);
    render(<GitHubAdmin />);
    await waitFor(() => expect(sessionStorage.getItem(creation)).toBeNull());
    expect(request).toHaveBeenCalledWith(prefix + '/operations/' + operation, undefined);
    expect(sessionStorage.getItem(creation + ':binding')).toBeNull();
    expect(sessionStorage.getItem('builder-github:alpha::discovery')).toBeNull();
  });

  it('allows only an existing repository connection after a retained create receipt needs a new branch', async () => {
    const operation = 'retained-create';
    const creation = 'builder-onboarding-project:alpha';
    history.replaceState(null, '', entry + '&operation_id=' + operation);
    sessionStorage.setItem(creation, JSON.stringify({ name: 'Site', slug: 'site', operation: 'registration-site' }));
    sessionStorage.setItem(creation + ':binding', operation);
    sessionStorage.setItem('builder-github:alpha:site', JSON.stringify({ intent: {
      operation_id: operation, role: 'visual', action: 'create', expected_role_revision: 7,
      expected_connection_revision: 0, target_account_id: 101, target_repository_name: 'site-repository',
    } }));
    const request = vi.fn(async (path: string) => {
      if (path === prefix) return reply(roles);
      if (path.endsWith('/finalize')) return new Response(JSON.stringify({
        teach: 'Vincule o repositório existente usando uma branch publicada.',
        teach_detail: { field_path: 'publication_branch' },
      }), { status: 409 });
      return reply({ state: 'awaiting_repository_access', operation_id: operation });
    });
    stubFetch(request);
    render(<GitHubAdmin />);
    fireEvent.click(await screen.findByRole('button', { name: 'Retomar confirmação' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Vincular repositório existente' }));
    expect(sessionStorage.getItem(creation)).toBeNull();
    expect(sessionStorage.getItem(creation + ':binding')).toBeNull();
    expect(sessionStorage.getItem('builder-github:alpha:site:existing-only')).toBe('1');
    expect(request.mock.calls.filter(([path]) => path.endsWith('/finalize'))).toHaveLength(1);
    expect(request.mock.calls.some(([path]) => path.endsWith('/abandon'))).toBe(false);
  });

  it('offers only repository selection after managed Visual creation recovery', async () => {
    vi.stubGlobal('IntersectionObserver', class {
      observe() {}
      disconnect() {}
    });
    history.replaceState(null, '', entry + '&managed_role=visual');
    sessionStorage.setItem('builder-github:alpha:site:existing-only:visual', '1');
    sessionStorage.setItem('builder-github:alpha:site:discovery', 'managed-discovery');
    stubFetch(async path => {
      if (path === prefix) return reply(roles);
      if (path.endsWith('/repositories?page=1')) return reply({
        repositories: [{ github_repository_id: 2, repository_full_name: 'alpha/recovered', default_branch: 'main' }], has_next: false,
      });
      expect(path).toBe(prefix + '/operations/managed-discovery');
      return reply({ state: 'verified', operation_id: 'managed-discovery', github_account_login: 'alpha', github_account_type: 'Organization' });
    });
    render(<GitHubAdmin />);
    const existing = await screen.findByRole('radio', { name: /Selecionar repositório existente/ });
    expect(existing).toBeChecked();
    expect(screen.queryByRole('radio', { name: /Criar novo repositório/ })).toBeNull();
    expect(screen.getByRole('link', { name: 'Voltar aos detalhes do projeto' })).toHaveAttribute('href', '/admin?page=projects&company_id=alpha&project_id=site');
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
    stubFetch(request);
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


describe('Company onboarding', () => {
  it('keeps the retry reachable when the new Project Company cannot be loaded', async () => {
    history.replaceState(null, '', '/admin/github?company_id=alpha&new_project=1');
    const request = vi.fn(async () => new Response('{"teach":"Empresa temporariamente indisponível."}', { status: 503 }));
    stubFetch(request);
    render(<AdminOnboarding />);
    const retry = await screen.findByRole('button', { name: 'Tentar novamente' });
    expect(retry.closest('[inert]')).toBeNull();
    fireEvent.click(retry);
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2));
  });

  it('retries the same Company independently and waits for directory readback', async () => {
    history.replaceState(null, '', '/admin');
    let reads = 0;
    let submitted: FormData | undefined;
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      expect(path).toBe('/admin/companies');
      if (init?.method !== 'POST') {
        reads++;
        return reply({
          companies: reads < 3 ? [] : [{ id: 'alpha-company', name: 'Álpha Company', has_image: false, projects: [] }],
          can_create_company: true,
        });
      }
      const body = init.body as FormData;
      expect(body.get('name')).toBe('Álpha Company');
      expect(body.get('slug')).toBe('alpha-company');
      expect(body.has('image')).toBe(false);
      if (submitted) expect(body.get('operation_id')).toBe(submitted.get('operation_id'));
      submitted = body;
      return reply({ id: 'alpha-company' });
    });
    stubFetch(request);
    render(<AdminOnboarding />);
    fireEvent.click(await screen.findByRole('button', { name: '＋ Nova empresa' }));
    fireEvent.change(screen.getByLabelText(/Nome da empresa/), { target: { value: 'Álpha Company' } });
    expect(screen.getByLabelText(/Slug da empresa/)).toHaveValue('alpha-company');
    fireEvent.click(screen.getByRole('button', { name: 'Salvar empresa' }));
    expect(await screen.findByText(/ainda não aparece na lista/)).toBeInTheDocument();
    expect(screen.queryByText('✓ Empresa salva')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Salvar empresa' }));
    expect(await screen.findByText('✓ Empresa salva')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Álpha Company/ })).toHaveAttribute('href', '/admin?company_id=alpha-company');
  });
});
