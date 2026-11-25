import { FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import './github-admin.css';

type Role = 'visual' | 'implementation';
type Action = 'connect' | 'create';
type Repository = { github_repository_id: number; repository_full_name: string; default_branch: string; connection_revision?: number };
type LinkedRepository = { repository_id?: string; github_repository_id?: number; github_account_id?: number; connection_revision?: number };
type Roles = { visual: LinkedRepository & { revision: number; publication_branch?: string }; implementation: { revision: number; repositories: LinkedRepository[] } };
type Operation = { state: string; operation_id: string; authorization_url?: string; github_account_login?: string; repository?: Repository };
type Intent = {
  operation_id: string; role: Role; action: Action; expected_role_revision: number;
  expected_connection_revision: number; target_account_id: number;
  target_repository_name?: string; publication_branch?: string;
};
type Draft = { intent: Intent; selected?: Repository };

async function api<T>(path: string, data?: object): Promise<T> {
  const response = await fetch(path, data ? {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data),
  } : undefined);
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { teach?: string };
    throw new Error(failure.teach || 'Não foi possível concluir esta etapa. Verifique seu acesso e tente novamente.');
  }
  return await response.json() as T;
}

function savedDraft(key: string): Draft | null {
  try {
    const draft = JSON.parse(sessionStorage.getItem(key) || 'null') as Draft | null;
    return draft?.intent?.operation_id ? draft : null;
  } catch { return null; }
}

function remember(key: string, draft: Draft | null) {
  if (draft) sessionStorage.setItem(key, JSON.stringify(draft));
  else sessionStorage.removeItem(key);
}

function goToGitHub(raw: string) {
  const destination = new URL(raw);
  const oauth = destination.pathname === '/login/oauth/authorize'
    && destination.searchParams.get('redirect_uri') === location.origin + '/admin/github/oauth/callback';
  const setup = /^\/apps\/[a-z0-9][a-z0-9-]*\/installations\/new$/.test(destination.pathname);
  if (destination.origin !== 'https://github.com' || destination.username || destination.password ||
    !destination.searchParams.get('state') || !(oauth || setup)) {
    throw new Error('O endereço de autorização não corresponde ao GitHub.');
  }
  location.assign(destination.href);
}

const stateLabels: Record<string, string> = {
  pending: 'Aguardando autorização no GitHub',
  verified: 'GitHub conectado',
  creating: 'Criação em andamento',
  awaiting_repository_access: 'Repositório criado. Aguardando acesso da instalação.',
  uncertain: 'O GitHub não confirmou o resultado da criação.',
  completed: 'Repositório vinculado',
  failed: 'Operação encerrada',
};

export function GitHubAdmin() {
  const params = new URLSearchParams(location.search);
  const company = params.get('company_id') || '';
  const project = params.get('project_id') || '';
  const [returnedOperation] = useState(() => params.get('operation_id'));
  const draftKey = `builder-github:${company}:${project}`;
  const base = `/admin/github/companies/${encodeURIComponent(company)}/projects/${encodeURIComponent(project)}`;
  const back = '/?' + new URLSearchParams({ company_id: company, project_id: project });
  const [draft, setDraft] = useState(() => savedDraft(draftKey));
  const [roles, setRoles] = useState<Roles | null>(null);
  const [operation, setOperation] = useState<Operation | null>(null);
  const [role, setRole] = useState<Role>('visual');
  const [action, setAction] = useState<Action>('connect');
  const [account, setAccount] = useState('');
  const [branch, setBranch] = useState('');
  const [name, setName] = useState('');
  const [link, setLink] = useState<LinkedRepository | null>(null);
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [page, setPage] = useState(0);
  const [hasNext, setHasNext] = useState(false);
  const [repositoryLoading, setRepositoryLoading] = useState(false);
  const [repositoryError, setRepositoryError] = useState('');
  const repositoryRequest = useRef(false);
  const repositoryEnd = useRef<HTMLDivElement>(null);
  const drawerHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { drawerHeading.current?.focus(); }, []);
  const [search, setSearch] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const [opID, setOpID] = useState(() => returnedOperation || draft?.intent.operation_id);
  const [initialOpID] = useState(opID);
  const opPath = base + '/operations/' + encodeURIComponent(opID || '');
  const currentDraft = draft?.intent.operation_id === opID ? draft : null;

  useEffect(() => {
    let active = true;
    const refresh = async () => {
      try {
        const currentRoles = await api<Roles>(base);
        if (!active) return;
        setRoles(currentRoles);
        setBranch(value => value || currentRoles.visual.publication_branch || '');
        if (initialOpID) {
          const status = await api<Operation>(base + '/operations/' + encodeURIComponent(initialOpID));
          if (active) setOperation(status);
        }
      } catch (failure) {
        if (active) setError(String((failure as Error).message));
      } finally { if (active) setLoaded(true); }
    };
    void refresh();
    return () => { active = false; };
  }, [base, initialOpID]);

  const report = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError('');
    try { await fn(); }
    catch (failure) { setError(String((failure as Error).message)); }
    finally { setBusy(false); }
  };
  const process = (result: Operation) => {
    setOpID(result.operation_id);
    setOperation(result);
    if (result.authorization_url) goToGitHub(result.authorization_url);
  };
  const start = async (next: Draft) => {
    remember(draftKey, next);
    setDraft(next);
    setOpID(next.intent.operation_id);
    // Store only the stable operation locator in the URL for safe reloads.
    history.replaceState(null, '', '/admin/github?' + new URLSearchParams({
      company_id: company, project_id: project, operation_id: next.intent.operation_id,
    }));
    process(await api<Operation>(base + '/operations', next.intent));
  };
  const begin = (event: FormEvent) => {
    event.preventDefault();
    if (!roles) return;
    void report(async () => {
      const targetAccount = Number(account);
      if (!Number.isSafeInteger(targetAccount) || targetAccount <= 0) throw new Error('Informe o ID numérico da conta GitHub.');
      const intent: Intent = {
        operation_id: 'github-' + crypto.randomUUID(), role, action,
        expected_role_revision: roles[role].revision,
        expected_connection_revision: action === 'connect' ? link?.connection_revision || 0 : 0,
        target_account_id: targetAccount,
        ...(action === 'create' ? { target_repository_name: name.trim() } : {}),
        ...(role === 'visual' && branch.trim() ? { publication_branch: branch.trim() } : {}),
      };
      await start({ intent });
    });
  };
  const choosingRepository = operation?.state === 'verified' && currentDraft?.intent.action === 'connect';
  const loadRepositories = useCallback(async (nextPage: number) => {
    if (repositoryRequest.current) return;
    repositoryRequest.current = true;
    setRepositoryLoading(true);
    setRepositoryError('');
    try {
      const result = await api<{ repositories: Repository[]; has_next: boolean }>(opPath + '/repositories?page=' + nextPage);
      setRepositories(previous => nextPage === 1 ? result.repositories : [...previous, ...result.repositories]);
      setPage(nextPage);
      setHasNext(result.has_next);
    } catch (failure) {
      setRepositoryError((failure as Error).message);
    } finally {
      repositoryRequest.current = false;
      setRepositoryLoading(false);
    }
  }, [opPath]);
  useEffect(() => {
    const end = repositoryEnd.current;
    if (!choosingRepository || repositoryLoading || repositoryError || !end) return;
    let active = true;
    const observer = new IntersectionObserver(entries => {
      if (active && entries.some(entry => entry.isIntersecting)) {
        active = false;
        observer.disconnect();
        void loadRepositories(page + 1);
      }
    });
    observer.observe(end);
    return () => {
      active = false;
      observer.disconnect();
    };
  }, [choosingRepository, loadRepositories, page, repositoryError, repositoryLoading]);
  const select = (repository: Repository) => {
    if (!currentDraft) return;
    const next = { ...currentDraft, selected: repository };
    remember(draftKey, next);
    setDraft(next);
  };
  const finish = () => void report(async () => {
    if (!currentDraft) return;
    const { intent, selected } = currentDraft;
    if (intent.action === 'connect' && !selected) throw new Error('Escolha um repositório.');
    if (selected && (selected.connection_revision || 0) !== intent.expected_connection_revision) {
      await start({ selected, intent: {
        ...intent, operation_id: 'github-' + crypto.randomUUID(),
        expected_connection_revision: selected.connection_revision || 0,
      } });
      return;
    }
    process(await api<Operation>(opPath + '/finalize', {
      expected_role_revision: intent.expected_role_revision,
      expected_connection_revision: intent.expected_connection_revision,
      ...(intent.action === 'connect' && selected ? { github_repository_id: selected.github_repository_id } : {}),
    }));
  });
  const clear = () => {
    remember(draftKey, null);
    location.replace('/admin/github?' + new URLSearchParams({ company_id: company, project_id: project }));
  };
  const allowDraftEntry = !opID;
  const selectable = repositories.filter(repository => repository.repository_full_name.toLowerCase().includes(search.toLowerCase()));
  const existing = role === 'visual' ? (roles?.visual.repository_id ? [roles.visual] : []) : roles?.implementation.repositories || [];

  return <div className="github-admin-page">
    <section className="github-admin-drawer" role="dialog" aria-modal="true" aria-labelledby="github-admin-heading">
      <header className="github-admin-head"><div><a href={back}>← Projetos</a><h1 id="github-admin-heading" ref={drawerHeading} tabIndex={-1}>Conectar repositório</h1></div><a href={back} aria-label="Fechar">×</a></header>
      <div className="github-admin-body">
        <div className="github-admin-company"><span aria-hidden="true">{company.charAt(0).toUpperCase()}</span><div><strong>{project}</strong><p>{company}</p></div></div>
        <section className="github-admin-section" aria-labelledby="github-admin-section-heading">
          <h2 id="github-admin-section-heading">GitHub</h2>
          {error && <p role="alert" className="github-admin-error">{error}</p>}
          {busy && <p role="status" className="github-admin-loading">Carregando…</p>}
          {loaded && opID && !operation && currentDraft && <button type="button" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Tentar novamente</button>}
          {loaded && opID && !operation && !currentDraft && <p>Retome esta operação na aba em que ela foi iniciada.</p>}
          {!loaded && <p role="status">Carregando conexão…</p>}
          {loaded && allowDraftEntry && roles && <form id="github-admin-start" onSubmit={begin}>
            <div className="github-admin-card"><strong>Conecte sua conta do GitHub</strong><p>Escolha um repositório existente ou crie um novo.</p></div>
            <label>Papel do repositório<select value={role} onChange={event => {setRole(event.target.value as Role); setLink(null);}}>
              <option value="visual">Artefatos visuais</option><option value="implementation">Código e provas</option>
            </select></label>
            {existing.length > 0 && <label>Vínculo existente<select value={link?.repository_id || ''} onChange={event => {
              const linked = existing.find(item => item.repository_id === event.target.value) || null;
              setLink(linked);
              if (linked?.github_account_id) setAccount(String(linked.github_account_id));
            }}><option value="">Escolher outro repositório</option>{existing.map(item => <option key={item.repository_id} value={item.repository_id}>{item.repository_id}</option>)}</select></label>}
            <fieldset className="github-admin-choices"><legend>Repositório</legend>
              <label className="github-admin-choice"><input type="radio" name="repo-path" checked={action === 'connect'} onChange={() => setAction('connect')} /><span><strong>Selecionar repositório existente</strong><small>Escolha um repositório disponível na conta.</small></span></label>
              <label className="github-admin-choice"><input type="radio" name="repo-path" checked={action === 'create'} onChange={() => {setAction('create');setLink(null);}} /><span><strong>Criar novo repositório</strong><small>Crie um repositório privado em uma organização GitHub.</small></span></label>
            </fieldset>
            <label>ID numérico da conta ou organização<input required inputMode="numeric" pattern="[0-9]+" value={account} onChange={event => setAccount(event.target.value)} /></label>
            {action === 'create' && <label>Nome do repositório<input required value={name} maxLength={100} onChange={event => setName(event.target.value)} /></label>}
            {role === 'visual' && <label>Branch de publicação<input required={action === 'connect'} value={branch} placeholder={action === 'create' ? 'Usar branch padrão' : 'main'} onChange={event => setBranch(event.target.value)} /></label>}
          </form>}
          {operation && <div className="github-admin-flow">
            <div className="github-admin-card" role="status"><strong>{stateLabels[operation.state] || 'Consultar conexão'}</strong>{operation.repository && <p>{operation.repository.repository_full_name}</p>}</div>
            {operation.github_account_login && <div className="github-admin-card">
              <strong>Conta GitHub: {operation.github_account_login}</strong>
              {choosingRepository && <p><button type="button" onClick={clear}>Trocar conta</button></p>}
            </div>}
            {!currentDraft && operation.state !== 'completed' && <p>Retome esta operação na aba em que ela foi iniciada.</p>}
            {currentDraft && choosingRepository && <>
              <label>Buscar repositórios<input value={search} placeholder="Buscar repositórios" onChange={event => setSearch(event.target.value)} /></label>
              {page > 0 && !hasNext && selectable.length === 0 && <p>Nenhum repositório nesta busca.</p>}
              {selectable.map(repo => <label key={repo.github_repository_id} className="github-admin-choice"><input type="radio" name="repository" checked={currentDraft.selected?.github_repository_id === repo.github_repository_id} onChange={() => select(repo)} /><span><strong>{repo.repository_full_name}</strong><small>{repo.default_branch}</small></span></label>)}
              {(page === 0 || hasNext) && <div ref={repositoryEnd}>
                {repositoryLoading && <p role="status">Carregando repositórios…</p>}
                {repositoryError && <><p role="alert">{repositoryError}</p><button type="button" onClick={() => void loadRepositories(page + 1)}>Tentar carregar repositórios novamente</button></>}
              </div>}
            </>}
            {operation.state === 'awaiting_repository_access' && <p>Conceda acesso ao repositório nas configurações da instalação do GitHub App e retome a confirmação.</p>}
            {operation.state === 'uncertain' && currentDraft && <>
              <p>Confira se o repositório foi criado no GitHub. Esta operação não repetirá a criação.</p>
              <label className="github-admin-choice"><input type="checkbox" checked={acknowledged} onChange={event => setAcknowledged(event.target.checked)} /> Conferi o resultado no GitHub</label>
              <button type="button" disabled={busy || !acknowledged} onClick={() => void report(async () => process(await api<Operation>(opPath + '/abandon', { acknowledge_uncertain_result: true })))}>Encerrar operação</button>
            </>}
          </div>}
        </section>
      </div>
      <footer className="github-admin-foot">
        <a className="github-admin-secondary" href={back}>{operation?.state === 'completed' ? 'Voltar ao projeto' : 'Cancelar'}</a>
        {allowDraftEntry && loaded && roles && <button className="github-admin-primary" type="submit" form="github-admin-start" disabled={busy}>Conectar GitHub</button>}
        {currentDraft && operation?.state === 'pending' && <button type="button" className="github-admin-primary" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Retomar autorização</button>}
        {currentDraft && (operation?.state === 'verified' || operation?.state === 'awaiting_repository_access') && <>
          <button type="button" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Retomar autorização</button>
          <button type="button" className="github-admin-primary" disabled={busy || currentDraft.intent.action === 'connect' && !currentDraft.selected} onClick={finish}>{operation.state === 'awaiting_repository_access' ? 'Retomar confirmação' : currentDraft.intent.action === 'create' ? 'Criar repositório' : 'Confirmar vínculo'}</button>
        </>}
        {operation?.state === 'creating' && <button type="button" disabled={busy || !currentDraft} onClick={finish}>Verificar situação</button>}
        {(operation?.state === 'failed' || operation?.state === 'completed') && <button type="button" onClick={clear}>Nova conexão</button>}
      </footer>
    </section>
  </div>;
}
