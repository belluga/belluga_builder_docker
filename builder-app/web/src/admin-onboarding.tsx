import { FormEvent, useEffect, useRef, useState } from 'react';
import { AdminAPIError, adminAPI, administrationSlug, GitHubAdmin, managedGitHubRole } from './github-admin';
import './admin-onboarding.css';
import { AdminProjects } from './admin-onboarding-projects';

type AdminProject = { id: string; name: string; binding_revision?: number; roadmap_revision?: number };
type AdminCompany = { id: string; name: string; has_image: boolean; projects: AdminProject[] };
type Administration = { companies: AdminCompany[]; can_create_company: boolean };

function projectURL(company: string, project: string, page = 'overview') {
  return '/?' + new URLSearchParams({ company_id: company, project_id: project, page });
}

function CompanyMark({ company }: { company: AdminCompany }) {
  return <span className="onboarding-company-mark" aria-hidden="true">
    {company.has_image
      ? <img src={'/admin/companies/' + encodeURIComponent(company.id) + '/image'} alt="" />
      : company.name.charAt(0).toUpperCase()}
  </span>;
}

export function AdminOnboarding() {
  const [params] = useState(() => new URLSearchParams(location.search));
  const selectedCompany = params.get('company_id') || '';
  const managedLink = location.pathname === '/admin/github' && !!managedGitHubRole(params);
  const globalProjects = params.get('page') === 'projects' || managedLink;
  const originProject = params.get('origin_project_id') || '';
  const originCompany = params.get('origin_company_id') || selectedCompany;
  const openProject = location.pathname === '/admin/github' && params.get('new_project') === '1';
  const [data, setData] = useState<Administration | null>(null);
  const [search, setSearch] = useState('');
  const [createCompany, setCreateCompany] = useState(false);
  const [companySaved, setCompanySaved] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const query = !globalProjects && selectedCompany ? '?' + new URLSearchParams({ company_id: selectedCompany }) : '';
    adminAPI<Administration>('/admin/companies' + query, undefined, controller.signal)
      .then(result => { if (!controller.signal.aborted) setData(result); })
      .catch(failure => { if (!controller.signal.aborted) setError((failure as Error).message); });
    return () => controller.abort();
  }, [attempt, globalProjects, selectedCompany]);
  const company = data?.companies.find(item => item.id === selectedCompany);
  const projectSaved = company?.projects.some(project => project.id === params.get('created_project'));
  const status = companySaved ? 'Empresa salva' : projectSaved ? 'Projeto salvo' : '';
  const normalizedSearch = search.trim().toLocaleLowerCase();
  const companies = data?.companies.filter(item => (item.name + ' ' + item.id).toLocaleLowerCase().includes(normalizedSearch)) || [];
  const projects = company?.projects.filter(item => (item.name + ' ' + item.id).toLocaleLowerCase().includes(normalizedSearch)) || [];
  const newProject = '/admin/github?' + new URLSearchParams({ company_id: selectedCompany, new_project: '1' });
  const companiesURL = '/admin' + (originProject ? '?' + new URLSearchParams({ origin_project_id: originProject, origin_company_id: originCompany }) : '');
  const globalURL = '/admin?' + new URLSearchParams({ page: 'projects', ...(originProject ? { origin_project_id: originProject, origin_company_id: originCompany } : {}) });
  const directoryURL = (id: string) => '/admin?' + new URLSearchParams({ company_id: id, ...(originProject ? { origin_project_id: originProject, origin_company_id: originCompany } : {}) });
  const projectDrawerOpen = openProject && !!company;
  const drawerOpen = createCompany || projectDrawerOpen || managedLink;
  return <div className="builder-onboarding">
    <div inert={drawerOpen} aria-hidden={drawerOpen || undefined}>
      <header className="onboarding-header">
        <a className="onboarding-brand" href={companiesURL}><span aria-hidden="true">◆</span> Builder</a>
        <a href={originCompany && originProject ? projectURL(originCompany, originProject) : '/'}>← {originProject ? 'Voltar ao projeto' : 'Voltar ao Viewer'}</a>
      </header>
      <div className="onboarding-shell">
        <nav className="onboarding-nav" aria-label="Administração">
          <h2>Administração</h2>
          <a href={companiesURL} aria-current={!company && !globalProjects ? 'page' : undefined}>▦ Empresas</a>
          <a href={globalURL} aria-current={globalProjects || company ? 'page' : undefined}>▱ Projetos</a>
        </nav>
        {globalProjects ? <AdminProjects companies={data?.companies || []} /> : <main className="onboarding-main">
          {company && <>
            <a href={companiesURL}>← Voltar para Empresas</a>
            <div className="onboarding-company-context"><CompanyMark company={company} /><strong>{company.name}</strong></div>
          </>}
          <header className="onboarding-main-heading">
            <div><span>ADMINISTRAÇÃO</span><h1>{company ? 'Projetos' : 'Empresas'}</h1>
              <p>{company ? <>Gerencie os projetos da empresa <strong>{company.name}</strong>.</> : 'Gerencie as empresas que organizam projetos no Builder.'}</p></div>
            {!company && data?.can_create_company && <button className="onboarding-primary" onClick={() => setCreateCompany(true)}>＋ Nova empresa</button>}
          </header>
          {status && <p className="onboarding-notice" role="status">✓ {status}</p>}
          {error && <div role="alert" className="onboarding-warning">{error}<button onClick={() => { setError(''); setAttempt(value => value + 1); }}>Tentar novamente</button></div>}
          {!data && !error && <p role="status">Carregando…</p>}
          {data && selectedCompany && !company && <p role="alert">Empresa indisponível para esta sessão.</p>}
          {data && <div className="onboarding-tools">
            <input aria-label={company ? 'Buscar projetos' : 'Buscar empresas'} placeholder={company ? 'Buscar projetos' : 'Buscar empresas'} value={search} onChange={event => setSearch(event.target.value)} />
            {company && <a className="onboarding-primary" href={newProject}>＋ Novo projeto</a>}
          </div>}
          {data && (company ? <div className="onboarding-projects">
            {projects.map(project => <article className="onboarding-project-card" key={project.id}>
              <header><span aria-hidden="true" className="onboarding-company-mark">◈</span><h2>{project.name}</h2><a href={projectURL(company.id, project.id)}>↗ Abrir projeto</a></header>
              <div className="onboarding-shortcuts" aria-label={'Áreas do projeto ' + project.name}>
                {([['overview', '▥ Overview', true], ['design-system', '◈ Design System', !!project.binding_revision], ['prototypes', '▷ Protótipos', !!project.binding_revision], ['roadmap', '⌁ Roadmap', !!project.roadmap_revision]] as const)
                  .map(([page, label, available]) => available
                    ? <a key={page} href={projectURL(company.id, project.id, page)}>{label}</a>
                    : <span key={page} aria-disabled="true">{label}<small>Indisponível</small></span>)}
              </div>
            </article>)}
            {projects.length === 0 && <p>Nenhum projeto encontrado.</p>}
          </div> : <div className="onboarding-directory">
            <div className="onboarding-directory-head"><span>Empresa</span><span>Slug</span></div>
            {companies.map(item => <a className="onboarding-row" key={item.id} href={directoryURL(item.id)}><CompanyMark company={item} /><strong>{item.name}</strong><span>{item.id}</span></a>)}
            {companies.length === 0 && <p>Nenhuma empresa encontrada.</p>}
          </div>)}
        </main>}
      </div>
    </div>
    {createCompany && <CompanyForm onClose={() => setCreateCompany(false)} onCreated={async id => {
      const current = await adminAPI<Administration>('/admin/companies');
      if (!current.companies.some(item => item.id === id)) throw new Error('A empresa ainda não aparece na lista.');
      setData(current); setCreateCompany(false); setSearch(''); setCompanySaved(true);
    }} />}
    {openProject && company && <GitHubAdmin companyName={company.name} />}
    {managedLink && <GitHubAdmin />}
  </div>;
}

function CompanyForm({ onCreated, onClose }: { onCreated: (id: string) => Promise<void>; onClose: () => void }) {
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [slugEdited, setSlugEdited] = useState(false);
  const [image, setImage] = useState<File>();
  const [imageURL, setImageURL] = useState('');
  const [operation] = useState(() => 'company-' + crypto.randomUUID());
  const [busy, setBusy] = useState(false);
  const saving = useRef(false);
  const [awaitingReadback, setAwaitingReadback] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; slug?: string; image?: string }>({});
  const [error, setError] = useState('');
  useEffect(() => {
    if (!image) { setImageURL(''); return; }
    const url = URL.createObjectURL(image);
    setImageURL(url);
    return () => URL.revokeObjectURL(url);
  }, [image]);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (saving.current) return;
    const validation = {
      name: !name.trim() ? 'Informe o nome da empresa.' : [...name.trim()].length > 160 ? 'Use até 160 caracteres no nome.' : undefined,
      slug: !slug ? 'Informe o slug da empresa.' : !/^[a-z][a-z0-9-]{0,63}$/.test(slug) ? 'Comece com uma letra minúscula e use até 64 letras, números ou hífens.' : undefined,
      image: fieldErrors.image,
    };
    setFieldErrors(validation);
    if (Object.values(validation).some(Boolean)) return;
    saving.current = true;
    setError(''); setBusy(true);
    const form = new FormData();
    form.set('name', name.trim());
    form.set('slug', slug);
    form.set('operation_id', operation);
    if (image) form.set('image', image);
    try {
      const company = await adminAPI<{ id: string }>('/admin/companies', form);
      await onCreated(company.id);
    } catch (failure) {
      setAwaitingReadback(!(failure instanceof AdminAPIError) || failure.status >= 500);
      setError((failure as Error).message);
    } finally { saving.current = false; setBusy(false); }
  };
  const changed = () => setError('');
  const frozen = busy || awaitingReadback;
  return <div className="github-admin-page github-company-create">
    <section className="github-admin-drawer" role="dialog" aria-modal="true" aria-labelledby="onboarding-company-title">
      <header className="github-admin-head">
        <div><button className="onboarding-mobile-back" type="button" onClick={onClose}>← Empresas</button>
          <h1 id="onboarding-company-title">Nova empresa</h1></div>
        <button type="button" aria-label="Fechar" onClick={onClose}>×</button>
      </header>
      <form className="github-admin-body github-admin-fields" id="onboarding-company-form" onSubmit={save} noValidate>
        {error && <div role="alert" className="onboarding-warning"><strong>Não foi possível salvar</strong><p>{error} Tente novamente. Os dados preenchidos foram mantidos.</p></div>}
        <label>Nome da empresa <span aria-hidden="true">*</span><input aria-invalid={!!fieldErrors.name} autoFocus disabled={frozen} value={name} onChange={event => {
          changed(); setName(event.target.value); if (!slugEdited) setSlug(administrationSlug(event.target.value));
          setFieldErrors(previous => ({ ...previous, name: undefined }));
        }} /></label>
        {fieldErrors.name && <p role="alert">{fieldErrors.name}</p>}
        <label>Slug da empresa <span aria-hidden="true">*</span><input aria-invalid={!!fieldErrors.slug} disabled={frozen} value={slug} onChange={event => {
          changed(); setSlugEdited(true); setSlug(event.target.value); setFieldErrors(previous => ({ ...previous, slug: undefined }));
        }} /></label>
        {fieldErrors.slug && <p role="alert">{fieldErrors.slug}</p>}
        <p>Identificador usado em endereços e integrações.</p>
        <strong>Imagem da empresa</strong>
        <div className="onboarding-image-row">
          <span className="onboarding-company-mark">{imageURL ? <img src={imageURL} alt="Prévia da imagem" /> : (name.charAt(0) || 'B').toUpperCase()}</span>
          <div><label className="onboarding-image-picker">Escolher imagem<input type="file" disabled={frozen} accept="image/png,image/jpeg,image/webp" onChange={event => {
            changed();
            const file = event.target.files?.[0];
            if (file && (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type) || file.size > 256 * 1024)) {
              setFieldErrors(previous => ({ ...previous, image: 'Use PNG, JPEG ou WebP com até 256 KiB.' }));
              return;
            }
            setFieldErrors(previous => ({ ...previous, image: undefined }));
            setImage(file);
          }} /></label>
          {(image || fieldErrors.image) && <button type="button" disabled={frozen} onClick={() => { changed(); setImage(undefined); setFieldErrors(previous => ({ ...previous, image: undefined })); }}>Remover imagem</button>}</div>
        </div>
        {fieldErrors.image && <p role="alert">{fieldErrors.image}</p>}
        <p>Imagem opcional, exibida em formato quadrado.</p>
      </form>
      <footer className="github-admin-foot">
        <button type="button" disabled={busy} onClick={onClose}>Cancelar</button>
        <button className="github-admin-primary" type="submit" form="onboarding-company-form" disabled={busy}>Salvar empresa</button>
      </footer>
    </section>
  </div>;
}
