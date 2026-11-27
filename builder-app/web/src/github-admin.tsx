import { useCallback, useEffect, useRef, useState } from 'react';
import './github-admin.css';

type Role = 'visual' | 'implementation';
type Action = 'connect' | 'create';
type Visibility = 'private' | 'public';
type Repository = { github_repository_id: number; repository_full_name: string; default_branch: string; connection_revision?: number };
type LinkedRepository = { repository_id?: string; github_repository_id?: number; github_account_id?: number; connection_revision?: number };
type Roles = { visual: LinkedRepository & { revision: number; publication_branch?: string }; implementation: { revision: number; repositories: LinkedRepository[] } };
type Installation = { installation_id: number; login: string; type: string; settings_url?: string };
type Operation = { installation_requested_at?: string; expires_at?: string; state: string; operation_id: string; project_id?: string; installations?: Installation[]; discovery_expired?: boolean; authorization_url?: string; settings_url?: string; github_account_login?: string; github_account_type?: string; repository?: Repository };
type Intent = {
  operation_id: string; role: Role; action: Action; expected_role_revision: number;
  expected_connection_revision: number; target_account_id?: number; discovery_operation_id?: string; visibility?: Visibility;
  target_repository_name?: string; publication_branch?: string;
};
type Draft = { intent: Intent; selected?: Repository };

export class AdminAPIError extends Error {
  constructor(message: string, readonly status: number, readonly field?: string) {
    super(message);
  }
}

export async function adminAPI<T>(path: string, data?: object, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, data ? {
    method: 'POST',
    ...(data instanceof FormData ? { body: data } : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) }),
    signal,
  } : signal ? { signal } : undefined);
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { teach?: string; teach_detail?: { field_path?: string } };
    throw new AdminAPIError(failure.teach || 'Não foi possível concluir esta etapa. Verifique seu acesso e tente novamente.', response.status, failure.teach_detail?.field_path);
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

function InstallationSettingsLink({ url }: { url?: string }) {
  if (!url || !/^https:\/\/github\.com(?:\/organizations\/[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)?\/settings\/installations\/[1-9][0-9]*$/.test(url)) return null;
  return <a className="github-settings-link" href={url} target="_blank" rel="noopener noreferrer">Configurar acesso</a>;
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

type ProjectFields = { name: string; slug: string; slugEdited: boolean; operation: string };
function projectFields(key: string): ProjectFields {
  try {
    const draft = JSON.parse(sessionStorage.getItem(key) || 'null') as ProjectFields | null;
    if (draft?.operation) return draft;
  } catch { /* Start a new draft. */ }
  return { name: '', slug: '', slugEdited: false, operation: 'project-' + crypto.randomUUID() };
}

export function administrationSlug(value: string) {
  return value.normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64);
}

function validProjectID(value: unknown): value is string {
  return typeof value === 'string' && /^[a-z][a-z0-9-]{0,63}$/.test(value) && !/[^a-z0-9-]/.test(value);
}

// Match Registry's repository component syntax and keep GitHub's creation limit.
function validRepositoryName(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= 100 && !/[^A-Za-z0-9._-]/.test(value) &&
    !value.startsWith('.') && !value.endsWith('.') && !value.includes('..');
}

const repositoryNameHint = 'Use até 100 caracteres: letras sem acento, números, hífen, sublinhado ou ponto. Evite espaços e pontos no início, no fim ou em sequência.';

export function managedGitHubRole(params: URLSearchParams): Role | null {
  const selected = params.get('managed_role');
  if (selected === 'visual' || selected === 'implementation') return selected;
  if (!params.has('operation_id')) return null;
  const key = `builder-github:${params.get('company_id') || ''}:${params.get('project_id') || ''}:managed-role`;
  const resumed = sessionStorage.getItem(key);
  return resumed === 'visual' || resumed === 'implementation' ? resumed : null;
}

export function GitHubAdmin({ companyName }: { companyName?: string } = {}) {
  const [params] = useState(() => new URLSearchParams(location.search));
  const company = params.get('company_id') || '';
  const projectID = params.get('project_id');
  const project = validProjectID(projectID) ? projectID : '';
  const draftKey = `builder-github:${company}:${project}`;
  const [managedRole] = useState(() => managedGitHubRole(params));
  useEffect(() => {
    if (managedRole) sessionStorage.setItem(draftKey + ':managed-role', managedRole);
  }, [draftKey, managedRole]);
  const existingOnlySuffix = ':existing-only' + (managedRole ? ':' + managedRole : '');
  const [existingOnly] = useState(() => sessionStorage.getItem(draftKey + existingOnlySuffix) === '1');
  const newProject = !project && params.get('new_project') === '1';
  const companyPath = '/admin/companies/' + encodeURIComponent(company);
  const projectDraftKey = 'builder-onboarding-project:' + company;
  const [fromGlobalProjects] = useState(() => params.get('from') === 'projects' ||
    params.has('operation_id') && sessionStorage.getItem(projectDraftKey + ':from-projects') === '1');
  useEffect(() => {
    if (newProject) {
      if (fromGlobalProjects) sessionStorage.setItem(projectDraftKey + ':from-projects', '1');
      else sessionStorage.removeItem(projectDraftKey + ':from-projects');
    }
  }, [fromGlobalProjects, newProject, projectDraftKey]);
  const [fields, setFields] = useState(() => projectFields(projectDraftKey));
  const [projectFieldsTouched, setProjectFieldsTouched] = useState({ name: false, slug: false });
  const validProjectName = !!fields.name.trim() && [...fields.name.trim()].length <= 160;
  const validProjectSlug = validProjectID(fields.slug);
  const [returnedOperation] = useState(() => params.get('operation_id'));
  const base = `/admin/github/companies/${encodeURIComponent(company)}/projects/${encodeURIComponent(project)}`;
  const back = managedRole ? '/admin?' + new URLSearchParams({ page: 'projects', company_id: company, project_id: project }) : newProject ? (fromGlobalProjects ? '/admin?page=projects' : '/admin?company_id=' + encodeURIComponent(company)) : '/?' + new URLSearchParams({ company_id: company, project_id: project });
  const [draft, setDraft] = useState(() => {
    const previous = savedDraft(draftKey);
    return managedRole && previous?.intent.role !== managedRole ? null : previous;
  });
  const invalidSavedRepository = draft?.intent.action === 'create' && !validRepositoryName(draft.intent.target_repository_name || '');
  const [roles, setRoles] = useState<Roles | null>(null);
  const [operation, setOperation] = useState<Operation | null>(null);
  const [role, setRole] = useState<Role>(managedRole || 'visual');
  const [action, setAction] = useState<Action | ''>(existingOnly ? 'connect' : '');
  const [canBindExistingReceipt, setCanBindExistingReceipt] = useState(false);
  const [visibility, setVisibility] = useState<Visibility | ''>(managedRole ? 'private' : '');
  const [discovery, setDiscovery] = useState<Operation | null>(null);
  const [discoveryID, setDiscoveryID] = useState(() => (newProject && returnedOperation) || sessionStorage.getItem(draftKey + ':discovery') || '');
  const [pendingInstallation, setPendingInstallation] = useState<Operation | null>(null);
  const [pendingLookupError, setPendingLookupError] = useState(false);
  const [returnedOperationUnavailable, setReturnedOperationUnavailable] = useState(false);
  const [selected, setSelected] = useState<Repository | null>(null);
  const [boundProject, setBoundProject] = useState(project);
  const [name, setName] = useState(() => {
    const previousName = draft?.intent.target_repository_name;
    return invalidSavedRepository && typeof previousName === 'string' ? previousName : '';
  });
  const [repositoryNameTouched, setRepositoryNameTouched] = useState(!!invalidSavedRepository);
  const repositoryNameError = repositoryNameTouched && !validRepositoryName(name);
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [page, setPage] = useState(0);
  const [hasNext, setHasNext] = useState(false);
  const [repositoryLoading, setRepositoryLoading] = useState(false);
  const [repositoryError, setRepositoryError] = useState('');
  const repositoryRequest = useRef<AbortController | null>(null);
  const repositoryEnd = useRef<HTMLDivElement>(null);
  const drawerHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { drawerHeading.current?.focus(); }, []);
  const [search, setSearch] = useState('');
  const [busy, setBusy] = useState(false);
  const mutationInFlight = useRef(false);
  const [error, setError] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const [opID, setOpID] = useState(() => project
    ? (returnedOperation === discoveryID ? null : returnedOperation) || (!invalidSavedRepository ? draft?.intent.operation_id : undefined)
    : undefined);
  const discoveryBase = newProject ? companyPath + '/github-discovery' : base + '/operations';
  const discoveryPath = discoveryBase + '/' + encodeURIComponent(discoveryID);
  const pendingPath = newProject ? companyPath + '/github-pending-installation' : base + '/pending-installation';
  const [initialOpID] = useState(opID);
  const bindingBase = '/admin/github/companies/' + encodeURIComponent(company) + '/projects/' + encodeURIComponent(boundProject);
  const opPath = bindingBase + '/operations/' + encodeURIComponent(opID || '');
  const currentDraft = !invalidSavedRepository && draft?.intent.operation_id === opID ? draft : null;

  const clearCreationDraft = useCallback(() => {
    sessionStorage.removeItem(projectDraftKey);
    sessionStorage.removeItem(projectDraftKey + ':binding');
    sessionStorage.removeItem(projectDraftKey + ':from-projects');
    sessionStorage.removeItem(`builder-github:${company}::discovery`);
    remember(`builder-github:${company}:`, null);
  }, [company, projectDraftKey]);

  const settleProjectCreation = useCallback((result: Operation, target: string) => {
    if (result.state !== 'completed' || sessionStorage.getItem(projectDraftKey + ':binding') !== result.operation_id) return false;
    const created = result.project_id || target;
    if (!validProjectID(created)) return false;
    clearCreationDraft();
    remember(`builder-github:${company}:${created}`, null);
    location.assign('/admin?' + new URLSearchParams({ ...(fromGlobalProjects ? { page: 'projects' } : { company_id: company }), created_project: created }));
    return true;
  }, [clearCreationDraft, company, fromGlobalProjects, projectDraftKey]);

  useEffect(() => {
    let active = true;
    const resumeRequest = new AbortController();
    const refresh = async () => {
      try {
        const currentRoles = newProject ? { visual: { revision: 0 }, implementation: { revision: 0, repositories: [] } } : await adminAPI<Roles>(base);
        if (!active) return;
        setRoles(currentRoles);
        if (!initialOpID) {
          let existing: Operation | null = null;
          if (discoveryID) {
            existing = await adminAPI<Operation>(discoveryPath).catch(failure => {
              if (failure instanceof AdminAPIError && failure.status === 404) {
                if (active && returnedOperation === discoveryID) setReturnedOperationUnavailable(true);
                return null;
              }
              throw failure;
            });
            if (!active) return;
            if (existing?.state === 'awaiting_organization_approval') {
              setPendingInstallation(existing);
              sessionStorage.removeItem(draftKey + ':discovery');
              setDiscoveryID('');
            } else if (existing && !existing.discovery_expired) setDiscovery(existing);
            else {
              sessionStorage.removeItem(draftKey + ':discovery');
              setDiscoveryID('');
            }
          }
          if (!existing || existing.discovery_expired) {
            const id = 'github-' + crypto.randomUUID();
            const status = await adminAPI<Operation>(discoveryBase, { action: 'resume-discovery', operation_id: id }, resumeRequest.signal);
            if (active && status?.installations) {
              sessionStorage.setItem(draftKey + ':discovery', id);
              setDiscoveryID(id);
              setDiscovery(status);
            }
          }
        }
        if (initialOpID) {
          const status = await adminAPI<Operation>(base + '/operations/' + encodeURIComponent(initialOpID)).catch(failure => {
            if (returnedOperation === initialOpID && !draft && failure instanceof AdminAPIError && failure.status === 404) return null;
            throw failure;
          });
          if (!active) return;
          if (!status) {
            setReturnedOperationUnavailable(true);
            setOpID(undefined);
            return;
          }
          if (status.state === 'awaiting_organization_approval') {
            setPendingInstallation(status);
            setOpID(undefined);
          } else if (!settleProjectCreation(status, project)) setOperation(status);
        }
      } catch (failure) {
        if (active) setError(String((failure as Error).message));
      } finally { if (active) setLoaded(true); }
    };
    void refresh();
    return () => { active = false; resumeRequest.abort(); };
  }, [base, initialOpID, newProject, project, settleProjectCreation]);

  useEffect(() => {
    if (!loaded || !roles) return;
    const controller = new AbortController();
    const restorePending = async () => {
      try {
        const result = await adminAPI<{ operation: Operation | null }>(pendingPath, undefined, controller.signal);
        if (!controller.signal.aborted && result.operation?.state === 'awaiting_organization_approval') {
          setPendingInstallation(current => current ?? result.operation);
        }
      } catch (failure) {
        if (!controller.signal.aborted && (!(failure instanceof AdminAPIError) || failure.status !== 404)) {
          setPendingLookupError(true);
        }
      }
    };
    void restorePending();
    return () => controller.abort();
  }, [loaded, pendingPath, roles]);

  const report = async (fn: () => Promise<void>) => {
    if (mutationInFlight.current) return;
    mutationInFlight.current = true;
    setBusy(true);
    setError('');
    try { await fn(); }
    catch (failure) { setError(String((failure as Error).message)); }
    finally { mutationInFlight.current = false; setBusy(false); }
  };
  const process = (result: Operation) => {
    if (settleProjectCreation(result, boundProject)) return;
    if (managedRole && result.state === 'completed') {
      remember(draftKey, null);
      sessionStorage.removeItem(draftKey + ':managed-role');
      sessionStorage.removeItem(draftKey + existingOnlySuffix);
      sessionStorage.removeItem(draftKey + ':discovery');
      location.replace(back + '&link_saved=1');
      return;
    }
    setOpID(result.operation_id);
    setOperation(result);
    if (result.authorization_url) goToGitHub(result.authorization_url);
  };
  const finalizeDraft = async (operationPath: string, next: Draft) => {
    const { intent, selected } = next;
    try {
      process(await adminAPI<Operation>(operationPath + '/finalize', {
        expected_role_revision: intent.expected_role_revision,
        expected_connection_revision: intent.expected_connection_revision,
        ...(intent.action === 'connect' && selected ? { github_repository_id: selected.github_repository_id } : {}),
      }));
    } catch (failure) {
      // The Registry may have recorded a terminal or in-flight state even if
      // the provider rejected the operation or the response was lost.
      const status = await adminAPI<Operation>(operationPath).catch(() => null);
      if (status) process(status);
      if (status?.state === 'awaiting_repository_access' && failure instanceof AdminAPIError &&
          (failure.field === 'publication_branch' || failure.field === 'expected_role_revision')) {
        setCanBindExistingReceipt(true);
      }
      if (status?.state !== 'completed') throw failure;
    }
  };
  const start = async (next: Draft) => {
    if (!validProjectID(boundProject)) throw new Error('O Registry ainda não confirmou o projeto. Volte ao formulário para retomar o cadastro.');
    if (next.intent.action === 'create' && !validRepositoryName(next.intent.target_repository_name || '')) throw new Error(repositoryNameHint);
    remember(draftKey, next);
    setDraft(next);
    setOpID(next.intent.operation_id);
    // Store only the stable operation locator in the URL for safe reloads.
    history.replaceState(null, '', '/admin/github?' + new URLSearchParams({
      company_id: company, project_id: boundProject, operation_id: next.intent.operation_id,
    }));
    process(await adminAPI<Operation>(bindingBase + '/operations', next.intent));
  };
  const saveFields = (next: ProjectFields) => {
    sessionStorage.setItem(projectDraftKey, JSON.stringify(next));
    setFields(next);
  };
  const openDiscovery = (reuse = false) => void report(async () => {
    setReturnedOperationUnavailable(false);
    const id = reuse && discoveryID ? discoveryID : 'github-' + crypto.randomUUID();
    repositoryRequest.current?.abort();
    repositoryRequest.current = null;
    setRepositoryLoading(false);
    setDiscoveryID(id);
    sessionStorage.setItem(draftKey + ':discovery', id);
    setDiscovery(null);
    setSelected(null);
    setAction(existingOnly ? 'connect' : '');
    setSearch('');
    setRepositories([]);
    setPage(0);
    setHasNext(false);
    setRepositoryError('');
    const status = await adminAPI<Operation>(discoveryBase, { action: 'discover', operation_id: id });
    setDiscovery(status);
    if (status.authorization_url) goToGitHub(status.authorization_url);
  });
  const chooseInstallation = (installation?: Installation) => void report(async () => {
    const status = await adminAPI<Operation>(discoveryBase, installation
      ? { action: 'select-installation', operation_id: discoveryID, installation_id: installation.installation_id }
      : { action: 'install-app', operation_id: discoveryID });
    if (status.authorization_url) goToGitHub(status.authorization_url);
    else setDiscovery(status);
  });
  const begin = () => void report(async () => {
    if (!roles || !action || !discoveryID || discovery?.state !== 'verified') return;
    if (action === 'connect' && !selected) throw new Error('Escolha um repositório.');
    if (action === 'create' && !validRepositoryName(name)) {
      setRepositoryNameTouched(true);
      return;
    }
    if (action === 'create' && !visibility) throw new Error('Escolha a visibilidade do repositório.');
    let target = project;
    if (newProject) {
      if (!validProjectName || !validProjectSlug) throw new Error('Informe o nome e um slug válido.');
      const saved = await adminAPI<{ id: string }>(companyPath + '/projects', {
        name: fields.name.trim(), slug: fields.slug, operation_id: fields.operation, discovery_operation_id: discoveryID,
      });
      if (!validProjectID(saved?.id) || saved.id !== fields.slug) throw new Error('O Registry ainda não confirmou o ID do projeto. Tente novamente para retomar o mesmo cadastro.');
      target = saved.id;
      setBoundProject(target);
    }
    const intent: Intent = {
      operation_id: discoveryID + '-binding', role: newProject ? 'visual' : role, action,
      expected_role_revision: roles[role].revision,
      expected_connection_revision: action === 'connect' ? selected?.connection_revision || 0 : 0,
      discovery_operation_id: discoveryID,
      ...(action === 'create' ? { target_repository_name: name, visibility: visibility as Visibility } : {}),
    };
    const next = { intent, selected: selected || undefined };
    remember(draftKey, next);
    remember(`builder-github:${company}:${target}`, next);
    setDraft(next);
    setOpID(intent.operation_id);
    const targetPath = `/admin/github/companies/${encodeURIComponent(company)}/projects/${encodeURIComponent(target)}/operations`;
    if (newProject) {
      sessionStorage.setItem(projectDraftKey + ':binding', intent.operation_id);
      history.replaceState(null, '', '/admin/github?' + new URLSearchParams({ company_id: company, project_id: target, operation_id: intent.operation_id }));
    }
    const started = await adminAPI<Operation>(targetPath, intent);
    process(started);
    if (!started.authorization_url && started.state === 'verified') {
      await finalizeDraft(targetPath + '/' + intent.operation_id, next);
    }
  });
  const choosingRepository = !opID && discovery?.state === 'verified' && action === 'connect' ||
    operation?.state === 'verified' && currentDraft?.intent.action === 'connect' && !currentDraft.intent.discovery_operation_id;
  const repositoryPath = !opID ? discoveryPath : opPath;
  const loadRepositories = useCallback(async (nextPage: number) => {
    if (repositoryRequest.current) return;
    const request = new AbortController();
    repositoryRequest.current = request;
    setRepositoryLoading(true);
    setRepositoryError('');
    try {
      const result = await adminAPI<{ repositories: Repository[]; has_next: boolean }>(repositoryPath + '/repositories?page=' + nextPage, undefined, request.signal);
      if (request.signal.aborted) return;
      setRepositories(previous => nextPage === 1 ? result.repositories : [...previous, ...result.repositories]);
      setPage(nextPage);
      setHasNext(result.has_next);
    } catch (failure) {
      if (!request.signal.aborted) setRepositoryError((failure as Error).message);
    } finally {
      if (repositoryRequest.current === request) {
        repositoryRequest.current = null;
        setRepositoryLoading(false);
      }
    }
  }, [repositoryPath]);
  const refreshRepositories = () => {
    repositoryRequest.current?.abort();
    repositoryRequest.current = null;
    setSelected(null);
    setRepositories([]);
    setPage(0);
    setHasNext(false);
    void loadRepositories(1);
  };
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
    if (!currentDraft) { setSelected(repository); return; }
    const next = { ...currentDraft, selected: repository };
    remember(draftKey, next);
    setDraft(next);
  };
  const finish = () => void report(async () => {
    if (!currentDraft) return;
    const { intent, selected } = currentDraft;
    if (intent.action === 'connect' && !selected) throw new Error('Escolha um repositório.');
    if (!intent.discovery_operation_id && selected && (selected.connection_revision || 0) !== intent.expected_connection_revision) {
      await start({ selected, intent: {
        ...intent, operation_id: 'github-' + crypto.randomUUID(),
        expected_connection_revision: selected.connection_revision || 0,
      } });
      return;
    }
    await finalizeDraft(opPath, currentDraft);
  });
  const clear = (selectExisting = false) => {
    if (!validProjectID(boundProject)) {
      remember(draftKey, null);
      setDraft(null);
      setOpID(undefined);
      setOperation(null);
      history.replaceState(null, '', '/admin/github?' + new URLSearchParams({ company_id: company, new_project: '1' }));
      return;
    }
    if (opID && sessionStorage.getItem(projectDraftKey + ':binding') === opID) clearCreationDraft();
    remember(draftKey, null);
    remember(`builder-github:${company}:${boundProject}`, null);
    sessionStorage.removeItem(draftKey + ':discovery');
    const entryMode = `builder-github:${company}:${boundProject}` + existingOnlySuffix;
    if (selectExisting) sessionStorage.setItem(entryMode, '1');
    else sessionStorage.removeItem(entryMode);
    location.replace('/admin/github?' + new URLSearchParams({ company_id: company, project_id: boundProject, ...(managedRole ? { managed_role: managedRole } : {}) }));
  };
  const allowDraftEntry = !opID;
  const linkedInRole = managedRole === 'implementation' ? roles?.implementation.repositories : managedRole === 'visual' && roles ? [roles.visual] : [];
  const selectable = repositories.filter(repository => repository.repository_full_name.toLowerCase().includes(search.toLowerCase()) &&
    !linkedInRole?.some(link => link.github_repository_id === repository.github_repository_id ||
      link.repository_id?.toLowerCase() === repository.repository_full_name.toLowerCase()));


  return <div className={newProject ? "github-admin-page github-project-create" : managedRole ? "github-admin-page github-managed-link" : "github-admin-page"}>
    <section className="github-admin-drawer" role="dialog" aria-modal="true" aria-labelledby="github-admin-heading">
      <header className="github-admin-head"><div><a className="github-admin-back" href={back} aria-label={managedRole ? 'Voltar aos detalhes do projeto' : undefined}>{managedRole ? '← Detalhes do projeto' : '← Projetos'}</a><h1 id="github-admin-heading" ref={drawerHeading} tabIndex={-1}>{newProject ? 'Novo projeto' : managedRole === 'implementation' ? 'Adicionar vínculo de código' : managedRole === 'visual' ? (roles?.visual.repository_id ? 'Substituir vínculo visual' : 'Adicionar vínculo visual') : 'Conectar repositório'}</h1></div><a href={back} aria-label="Fechar">×</a></header>
      <div className="github-admin-body">
        <div className="github-admin-company"><span aria-hidden="true">{company.charAt(0).toUpperCase()}</span><div><strong>{newProject ? companyName || company : project}</strong><p>{newProject ? 'Empresa selecionada' : company}</p></div></div>
        {newProject && <div className="github-admin-fields">
          <label>Nome do projeto <span aria-hidden="true">*</span><input required disabled={!!opID} aria-invalid={projectFieldsTouched.name && !validProjectName} onBlur={() => setProjectFieldsTouched(previous => ({ ...previous, name: true }))} value={fields.name} onChange={event => saveFields({ ...fields, name: event.target.value, slug: fields.slugEdited ? fields.slug : administrationSlug(event.target.value) })} /></label>
          {projectFieldsTouched.name && !validProjectName && <p role="alert">Informe o nome do projeto, com até 160 caracteres.</p>}
          <label>Slug do projeto <span aria-hidden="true">*</span><input required disabled={!!opID} aria-invalid={projectFieldsTouched.slug && !validProjectSlug} onBlur={() => setProjectFieldsTouched(previous => ({ ...previous, slug: true }))} value={fields.slug} onChange={event => saveFields({ ...fields, slug: event.target.value, slugEdited: true })} /></label>
          <p>Identificador usado em endereços e integrações.</p>
          {projectFieldsTouched.slug && !validProjectSlug && <p role="alert">{fields.slug ? 'Comece com uma letra minúscula e use até 64 letras, números ou hífens.' : 'Informe o slug do projeto.'}</p>}
        </div>}
        <section className="github-admin-section" aria-labelledby="github-admin-section-heading">
          <h2 id="github-admin-section-heading">GitHub</h2>
          {error && <p role="alert" className="github-admin-error">{error}</p>}
          {busy && <p role="status" className="github-admin-loading">Carregando…</p>}
          {loaded && opID && !operation && currentDraft && <button type="button" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Tentar novamente</button>}
          {loaded && opID && !operation && (currentDraft?.intent.discovery_operation_id || invalidSavedRepository) && <button type="button" disabled={busy} onClick={() => clear()}>Reconectar GitHub</button>}
          {loaded && opID && !operation && !currentDraft && <p>Retome esta operação na aba em que ela foi iniciada.</p>}
          {!loaded && <p role="status">Carregando conexão…</p>}
          {returnedOperationUnavailable && <p role="status">A solicitação anterior expirou ou não está disponível. Inicie uma nova conexão para continuar.</p>}
          {pendingLookupError && <p role="status">Não foi possível consultar solicitações de instalação anteriores.</p>}
          {loaded && pendingInstallation && <div className="github-admin-card" role="status">
            <strong>Aguardando aprovação da organização</strong>
            {pendingInstallation.installation_requested_at && <p>Solicitação registrada em <time dateTime={pendingInstallation.installation_requested_at}>
              {new Date(pendingInstallation.installation_requested_at).toLocaleDateString('pt-BR')}
            </time>.</p>}
            <p>Um proprietário da organização precisa aprovar a instalação do GitHub App. Depois, atualize as instalações e selecione a organização e o repositório.</p>
            {allowDraftEntry && <button className="github-admin-primary" type="button" disabled={busy} onClick={() => openDiscovery()}>Atualizar instalações</button>}
          </div>}
          {loaded && allowDraftEntry && roles && !pendingInstallation && discovery?.state !== 'verified' && !discovery?.installations && <div className="github-admin-card github-connect-card">
            <GitHubMark />
            <strong>Conecte sua conta do GitHub</strong>
            <p>{existingOnly ? 'Depois, escolha o repositório existente.' : 'Depois, escolha um repositório existente ou crie um novo.'}</p>
            <button className="github-admin-primary" type="button" disabled={busy} onClick={() => openDiscovery(!!discoveryID)}>Conectar GitHub</button>
            {discoveryID && error && <button type="button" disabled={busy} onClick={() => openDiscovery()}>Nova conexão</button>}
          </div>}
          {loaded && allowDraftEntry && roles && discovery?.installations && <div className="github-admin-flow">
            <div className="github-admin-card github-installation-picker">
              <strong>Escolha uma conta ou organização do GitHub</strong>
              {pendingInstallation && <p>Se a organização solicitada não aparecer, a instalação ou o acesso ao repositório ainda não foi confirmado.</p>}
              {!pendingInstallation && discovery.installations.length === 0 && <p>Nenhuma instalação do GitHub App está disponível para esta conta.</p>}
              {discovery.installations.map(installation => <div className="github-installation-row" key={installation.installation_id}>
                <button type="button" disabled={busy} onClick={() => chooseInstallation(installation)}>
                  <strong>@{installation.login}</strong>
                  <small>{installation.type === 'Organization' ? 'Organização' : 'Conta pessoal'}</small>
                </button>
                <InstallationSettingsLink url={installation.settings_url} />
              </div>)}
              <button type="button" disabled={busy} onClick={() => chooseInstallation()}>Instalar o GitHub App em outra conta</button>
              {error && <button type="button" disabled={busy} onClick={() => openDiscovery()}>Atualizar contas disponíveis</button>}
            </div>
          </div>}
          {loaded && allowDraftEntry && roles && discovery?.state === 'verified' && <div className="github-admin-flow">
            <div className="github-admin-card github-connected" role="status"><GitHubMark /><span className="github-connected-check" aria-hidden="true">✓</span><div><strong>GitHub conectado</strong><small>@{discovery.github_account_login}</small></div>
              <div className="github-connected-actions"><InstallationSettingsLink url={discovery.settings_url} />
                <button type="button" disabled={busy} onClick={() => openDiscovery()}>Trocar conta</button>
              </div></div>
            {!newProject && !managedRole && <label>Papel do repositório<select value={role} onChange={event => setRole(event.target.value as Role)}>
              <option value="visual">Artefatos visuais</option><option value="implementation">Código e provas</option>
            </select></label>}
            <p>{existingOnly ? 'Escolha o repositório existente.' : 'Escolha um repositório existente ou crie um novo.'}</p>
            <fieldset className="github-admin-choices" aria-label="Repositório">
              {!existingOnly && <label className="github-admin-choice"><input type="radio" name="repo-path" disabled={!!discovery.github_account_type && discovery.github_account_type !== 'Organization'} checked={action === 'create'} onChange={() => setAction('create')} /><span className="github-repo-icon" aria-hidden="true">＋</span><span><strong>Criar novo repositório</strong><small>{discovery.github_account_type === 'User' ? 'A criação de repositório está disponível em organizações GitHub.' : 'Crie um novo repositório no GitHub.'}</small></span></label>}
              {action === 'create' && <div className="github-repo-expansion github-repo-new">
                <label>Conta ou organização<select value={discovery.github_account_login} disabled><option>{discovery.github_account_login}</option></select></label>
                <label>Nome do repositório<input required value={name} aria-invalid={repositoryNameError} aria-describedby={repositoryNameError ? 'github-repository-name-error' : undefined}
                  onChange={event => { setName(event.target.value); setRepositoryNameTouched(true); }} /></label>
                {repositoryNameError && <p className="github-admin-error" id="github-repository-name-error" role="alert">{name ? repositoryNameHint : 'Informe o nome do repositório.'}</p>}
                <fieldset className="github-admin-choices github-visibility"><legend>Visibilidade <span aria-hidden="true">*</span></legend>
                  <label className="github-admin-choice"><input type="radio" name="visibility" checked={visibility === 'private'} onChange={() => setVisibility('private')} /><span><strong>Privado</strong><small>Apenas pessoas autorizadas.</small></span></label>
                  <label className="github-admin-choice"><input type="radio" name="visibility" checked={visibility === 'public'} onChange={() => setVisibility('public')} /><span><strong>Público</strong><small>Qualquer pessoa pode acessar.</small></span></label>
                </fieldset>
              </div>}
              <label className="github-admin-choice"><input type="radio" name="repo-path" checked={action === 'connect'} onChange={() => setAction('connect')} /><span className="github-repo-icon" aria-hidden="true">⌕</span><span><strong>Selecionar repositório existente</strong><small>Escolha um repositório disponível na conta.</small></span></label>
              {action === 'connect' && <div className="github-repo-expansion" aria-live="polite">
                <label><span className="github-visually-hidden">Buscar repositórios</span><input value={search} placeholder="Buscar repositórios" onChange={event => setSearch(event.target.value)} /></label>
                <button type="button" disabled={busy} onClick={refreshRepositories}>Atualizar repositórios</button>
                {page > 0 && !hasNext && selectable.length === 0 && <p className="github-repo-state">Nenhum repositório nesta busca.</p>}
                {selectable.map(repo => <label key={repo.github_repository_id} className="github-admin-choice github-repo-result"><input type="radio" name="repository" checked={selected?.github_repository_id === repo.github_repository_id} onChange={() => select(repo)} /><span><strong>{repo.repository_full_name}</strong><small>{repo.default_branch}</small></span></label>)}
                {(page === 0 || hasNext) && <div ref={repositoryEnd}>
                  {repositoryLoading && <p role="status">Carregando repositórios…</p>}
                  {repositoryError && <><p role="alert">{repositoryError}</p><button type="button" onClick={() => void loadRepositories(page + 1)}>Tentar carregar repositórios novamente</button><button type="button" disabled={busy} onClick={() => openDiscovery(true)}>Retomar autorização</button></>}
                </div>}
              </div>}
            </fieldset>
          </div>}
          {operation && <div className="github-admin-flow">
            <div className="github-admin-card" role="status"><strong>{stateLabels[operation.state] || 'Consultar conexão'}</strong>{operation.repository && <p>{operation.repository.repository_full_name}</p>}</div>
            {operation.github_account_login && <div className="github-admin-card">
              <strong>Conta GitHub: {operation.github_account_login}</strong>
              {choosingRepository && <p><button type="button" onClick={() => clear()}>Trocar conta</button></p>}
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
              <button type="button" disabled={busy || !acknowledged} onClick={() => void report(async () => process(await adminAPI<Operation>(opPath + '/abandon', { acknowledge_uncertain_result: true })))}>Encerrar operação</button>
            </>}
          </div>}
        </section>
      </div>
      <footer className="github-admin-foot">
        <a className="github-admin-secondary" href={back}>{operation?.state === 'completed' ? 'Voltar ao projeto' : 'Cancelar'}</a>
        {allowDraftEntry && loaded && roles && (newProject || discovery?.state === 'verified') && <button className="github-admin-primary" type="button" onClick={begin}
          disabled={busy || discovery?.state !== 'verified' || !action || action === 'connect' && !selected ||
            action === 'create' && (!validRepositoryName(name) || !visibility) || newProject && (!validProjectName || !validProjectSlug)}>
          {newProject ? 'Salvar' : action === 'create' ? 'Criar repositório' : 'Confirmar vínculo'}
        </button>}
        {currentDraft && operation?.state === 'pending' && <button type="button" className="github-admin-primary" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Retomar autorização</button>}
        {currentDraft && (operation?.state === 'verified' || operation?.state === 'awaiting_repository_access') && <>
          <button type="button" disabled={busy} onClick={() => void report(() => start(currentDraft))}>Retomar autorização</button>
          <button type="button" className="github-admin-primary" disabled={busy || currentDraft.intent.action === 'connect' && !currentDraft.selected} onClick={finish}>{operation.state === 'awaiting_repository_access' ? 'Retomar confirmação' : currentDraft.intent.action === 'create' ? 'Criar repositório' : 'Confirmar vínculo'}</button>
        </>}
        {operation?.state === 'awaiting_repository_access' && canBindExistingReceipt && <button type="button" onClick={() => clear(true)}>Vincular repositório existente</button>}
        {operation?.state === 'creating' && <button type="button" disabled={busy || !currentDraft} onClick={finish}>Verificar situação</button>}
        {(operation?.state === 'failed' || operation?.state === 'completed' || operation?.state === 'verified' && error) && <button type="button" onClick={() => clear()}>Nova conexão</button>}
      </footer>
    </section>
  </div>;
}

function GitHubMark() {
  return <svg className="github-brand-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="M12 .7a11.3 11.3 0 0 0-3.58 22.02c.57.1.78-.25.78-.55v-2.1c-3.18.7-3.85-1.35-3.85-1.35-.52-1.27-1.25-1.61-1.25-1.61-1.02-.73.08-.7.08-.7 1.14.08 1.74 1.17 1.74 1.17 1 1.73 2.64 1.23 3.28.94.1-.74.4-1.25.72-1.54-2.54-.29-5.21-1.27-5.21-5.6 0-1.25.44-2.26 1.16-3.06-.12-.28-.5-1.45.11-3 0 0 .95-.3 3.11 1.16a10.76 10.76 0 0 1 5.63 0c2.16-1.46 3.11-1.16 3.11-1.16.61 1.55.23 2.72.11 3 .72.8 1.16 1.81 1.16 3.06 0 4.34-2.67 5.31-5.22 5.6.41.35.78 1.05.78 2.12v3.12c0 .3.2.65.79.54A11.3 11.3 0 0 0 12 .7Z" /></svg>;
}
