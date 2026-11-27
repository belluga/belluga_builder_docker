import { useEffect, useRef, useState } from 'react';
import { AdminAPIError, adminAPI } from './github-admin';

type Project = { id: string; name: string; company_id: string; company_name: string };
type Page = { projects: Project[]; next_cursor?: string };

function projectDetails(project: Project) {
  return '/admin?' + new URLSearchParams({ page: 'projects', company_id: project.company_id, project_id: project.id });
}

export function AdminProjects({ companies }: { companies: { id: string; name: string }[] }) {
  const params = new URLSearchParams(location.search);
  const companyID = params.get('company_id') || '';
  const projectID = params.get('project_id') || '';
  const [filter, setFilter] = useState('');
  const [search, setSearch] = useState('');
  const created = params.has('created_project');
  const [creating, setCreating] = useState(false);
  const [creationCompany, setCreationCompany] = useState('');
  const active = companyID && projectID ? companyID + ':' + projectID : '';
  return <main className={'onboarding-main onboarding-global' + (active ? ' has-detail' : '')}>
    <section className="onboarding-global-list" aria-label="Lista de projetos">
      <header className="onboarding-main-heading"><h1>Projetos</h1></header>
      {created && <p className="onboarding-notice" role="status">Projeto salvo.</p>}
      <div className="onboarding-tools">
        <input aria-label="Buscar projetos" placeholder="Buscar projetos" maxLength={160} value={search} onChange={event => setSearch(event.target.value)} />
        {!active && <select aria-label="Filtrar por empresa" value={filter} onChange={event => setFilter(event.target.value)}>
          <option value="">Todas as empresas</option>
          {companies.map(company => <option key={company.id} value={company.id}>{company.name}</option>)}
        </select>}
        {!active && <button className="onboarding-primary" type="button" onClick={() => {
          setCreationCompany(filter || (companies.length === 1 ? companies[0].id : ''));
          setCreating(true);
        }}>Novo projeto</button>}
      </div>
      {creating && <div className="onboarding-tools" aria-label="Novo projeto">
        <select aria-label="Empresa do novo projeto" value={creationCompany} onChange={event => setCreationCompany(event.target.value)}>
          <option value="">Escolha a empresa</option>
          {companies.map(company => <option key={company.id} value={company.id}>{company.name}</option>)}
        </select>
        <button type="button" onClick={() => setCreating(false)}>Cancelar</button>
        <button type="button" disabled={!creationCompany} onClick={() => location.assign('/admin/github?' + new URLSearchParams({ company_id: creationCompany, new_project: '1', from: 'projects' }))}>Continuar</button>
      </div>}
      <ProjectList key={JSON.stringify([filter, search.trim()])} filter={filter} search={search.trim()} active={active} />
    </section>
    {companyID && projectID && <ProjectDetails company={companyID} project={projectID} companyName={companies.find(company => company.id === companyID)?.name} />}
  </main>;
}

function ProjectList({ filter, search, active }: { filter: string; search: string; active: string }) {
  const [rows, setRows] = useState<Project[]>([]);
  const [next, setNext] = useState<string>();
  const [cursor, setCursor] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    const query = new URLSearchParams();
    if (filter) query.set('company_id', filter);
    if (search) query.set('q', search);
    if (cursor) query.set('cursor', cursor);
    const delay = window.setTimeout(() => {
      adminAPI<Page>('/admin/projects?' + query, undefined, controller.signal).then(page => {
        if (controller.signal.aborted) return;
        setRows(previous => [...previous, ...page.projects]);
        setNext(page.next_cursor);
      }).catch(failure => {
        if (!controller.signal.aborted) setError((failure as Error).message);
      }).finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    }, search && !cursor ? 200 : 0);
    return () => { window.clearTimeout(delay); controller.abort(); };
  }, [attempt, cursor, filter, search]);

  useEffect(() => {
    if (!end.current || !next || loading || error || typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) {
        observer.disconnect();
        setLoading(true);
        setCursor(next);
      }
    });
    observer.observe(end.current);
    return () => observer.disconnect();
  }, [error, loading, next]);

  return <div className="onboarding-directory">
    {!active && <div className="onboarding-global-row onboarding-directory-head"><strong>Projeto</strong><strong>Empresa</strong></div>}
    {rows.map(project => {
      const id = project.company_id + ':' + project.id;
      return <a className="onboarding-global-row" key={id} href={projectDetails(project)} aria-current={active === id || undefined}>
        <strong>{project.name}</strong><span className="onboarding-company-badge">{project.company_name}</span><span aria-hidden="true">›</span>
      </a>;
    })}
    {loading && <p role="status">Carregando projetos…</p>}
    {!loading && !error && rows.length === 0 && <p>Nenhum projeto encontrado.</p>}
    {error && <div role="alert" className="onboarding-warning">{error}
      <button type="button" onClick={() => { setError(''); setLoading(true); setAttempt(value => value + 1); }}>Tentar novamente</button>
    </div>}
    {next && <div ref={end} aria-hidden="true" />}
  </div>;
}

type Link = { repository_id?: string; publication_branch?: string; connection_revision?: number };
type Roles = { project_name?: string; visual: Link & { revision: number }; implementation: { revision: number; repositories: Link[] } };

function ProjectDetails({ company, project, companyName }: { company: string; project: string; companyName?: string }) {
  const [roles, setRoles] = useState<Roles>();
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [restricted, setRestricted] = useState(false);
  const [staleRevision, setStaleRevision] = useState(false);
  const [notice, setNotice] = useState(new URLSearchParams(location.search).get('link_saved') === '1' ? 'Vínculo salvo.' : '');
  const [removal, setRemoval] = useState<{ role: 'visual' | 'implementation'; repository: string; revision: number; operation: string }>();
  const [busy, setBusy] = useState(false);
  const mutationInFlight = useRef(false);
  const confirmation = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    if (confirmation.current?.showModal) confirmation.current.showModal();
    else confirmation.current?.setAttribute('open', '');
  }, [removal]);
  const base = '/admin/github/companies/' + encodeURIComponent(company) + '/projects/' + encodeURIComponent(project);
  const back = '/admin?page=projects';
  const connect = (role: 'visual' | 'implementation') => '/admin/github?' + new URLSearchParams({ company_id: company, project_id: project, managed_role: role });
  useEffect(() => {
    const controller = new AbortController();
    adminAPI<Roles>(base, undefined, controller.signal)
      .then(result => { if (!controller.signal.aborted) setRoles(result); })
      .catch(failure => {
        if (controller.signal.aborted) return;
        if (failure instanceof AdminAPIError && (failure.status === 401 || failure.status === 403)) {
          setRestricted(true);
          return;
        }
        setError((failure as Error).message);
      });
    return () => controller.abort();
  }, [attempt, base]);
  const confirmRemoval = async () => {
    if (!removal || mutationInFlight.current) return;
    mutationInFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const result = await adminAPI<Roles>(base + '/remove', {
        role: removal.role, repository_id: removal.repository, expected_revision: removal.revision, operation_id: removal.operation,
      });
      setRoles(result);
      setRemoval(undefined);
      setNotice('Vínculo removido.');
    } catch (failure) {
      setError((failure as Error).message);
      if (failure instanceof AdminAPIError && failure.field === 'expected_revision') {
        setStaleRevision(true);
        setRemoval(undefined);
      }
    } finally {
      setBusy(false);
      mutationInFlight.current = false;
    }
  };
  const remove = (role: 'visual' | 'implementation', repository: string, revision: number) => {
    setError('');
    setRemoval({ role, repository, revision, operation: 'remove-' + crypto.randomUUID() });
  };
  return <section className="onboarding-project-detail" aria-label="Detalhes do projeto">
    <a className="onboarding-detail-back" href={back}>← Todos os projetos</a>
    <h1>{roles?.project_name || project}</h1>
    <span className="onboarding-company-badge">{companyName || company}</span>
    {' '}<a className="onboarding-detail-back" href={'/?' + new URLSearchParams({ company_id: company, project_id: project, page: 'overview' })}>Abrir projeto ↗</a>
    <h2>GitHub</h2>
    {error && !removal && <div role="alert" className="onboarding-warning">{error}
      {(!roles || staleRevision) && <button type="button" onClick={() => {
        setRoles(undefined);
        setError('');
        setStaleRevision(false);
        setAttempt(value => value + 1);
      }}>{staleRevision ? 'Recarregar vínculos' : 'Tentar novamente'}</button>}
    </div>}
    {notice && <p className="onboarding-notice" role="status">{notice}</p>}
    {restricted && <p role="status">Você não tem permissão para editar os vínculos deste projeto.</p>}
    {!restricted && !error && !roles && <p role="status">Carregando vínculos…</p>}
    {roles && !staleRevision && <>
      <section className="onboarding-directory onboarding-link-group"><h3>Visual</h3>
        {roles.visual.repository_id && <div className="onboarding-repository-line"><span><strong>{roles.visual.repository_id}</strong> <small className="onboarding-company-badge">{roles.visual.publication_branch}</small></span>
          <span className="onboarding-repository-actions">
            <a className="onboarding-link-action" href={connect('visual')} aria-label="Substituir vínculo visual">Substituir</a>
            <button type="button" className="onboarding-remove-link" disabled={!!removal} aria-label="Remover vínculo visual" onClick={() => remove('visual', roles.visual.repository_id!, roles.visual.revision)}>Remover</button>
          </span></div>}
        {!roles.visual.repository_id && <p className="onboarding-repository-line">Nenhum repositório vinculado.</p>}
        {!roles.visual.repository_id && <a className="onboarding-link-action" href={connect('visual')}>Adicionar vínculo visual</a>}
      </section>
      <section className="onboarding-directory onboarding-link-group"><h3>Código</h3>
        {roles.implementation.repositories.map(repo => repo.repository_id && <div className="onboarding-repository-line" key={repo.repository_id}><strong>{repo.repository_id}</strong>
          <button type="button" className="onboarding-remove-link" disabled={!!removal} aria-label={'Remover vínculo de código ' + repo.repository_id} onClick={() => remove('implementation', repo.repository_id!, roles.implementation.revision)}>Remover</button></div>)}
        {!roles.implementation.repositories.length && <p className="onboarding-repository-line">Nenhum repositório vinculado.</p>}
        <a className="onboarding-link-action" href={connect('implementation')} aria-label="Adicionar vínculo de código">Adicionar repositório</a>
      </section>
    </>}
    {removal && <dialog ref={confirmation} className="onboarding-removal" aria-labelledby="project-removal-heading" onCancel={event => {
      event.preventDefault();
      if (!mutationInFlight.current) { setRemoval(undefined); setError(''); }
    }}>
      <h2 id="project-removal-heading">Remover vínculo {removal.role === 'visual' ? 'visual' : 'de código'}?</h2>
      <p><strong>{removal.repository}</strong> · {roles?.project_name || project} · {companyName || company}</p>
      <p>{removal.role === 'visual' ? 'O Design System ficará sem vínculo e os demais artefatos perderão esta fonte.' : 'O projeto deixará de usar este repositório como fonte de código.'} O repositório continuará disponível no GitHub.</p>
      {error && <p role="alert" className="onboarding-warning">{error}</p>}
      <div className="onboarding-repository-actions">
        <button type="button" disabled={busy} autoFocus onClick={() => { setRemoval(undefined); setError(''); }}>Cancelar remoção</button>
        <button type="button" className="onboarding-primary" disabled={busy} onClick={() => void confirmRemoval()}>Confirmar remoção</button>
      </div>
    </dialog>}
  </section>;
}
