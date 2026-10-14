import { useEffect, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

type Doc = { id: string; title: string };
type Preview = { commit: string; landing_version: string; access_mode: string; remote_verification: string; local_integrity: string; bundle_fingerprint: string; documents: Doc[] };
type Document = { id: string; title: string; path: string; commit: string; markdown: string };

function documentIdFromPath() {
  const encoded = location.pathname.split('/').filter(Boolean)[1] ?? 'landing';
  try { return decodeURIComponent(encoded); }
  catch { return encoded; }
}

export function Viewer() {
  const [preview, setPreview] = useState<Preview | null>(null);
  const [selected, setSelected] = useState(documentIdFromPath);
  const [document, setDocument] = useState<Document | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/preview', { signal: controller.signal })
      .then((response) => { if (!response.ok) throw new Error('Não foi possível carregar o catálogo.'); return response.json() as Promise<Preview>; })
      .then(setPreview).catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Falha ao carregar a prévia.'); });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (!preview) return;
    const controller = new AbortController();
    const requested = selected;
    setLoading(true); setError(''); setDocument(null);
    fetch(`/api/preview/documents/${encodeURIComponent(requested)}`, { signal: controller.signal })
      .then((response) => { if (!response.ok) throw new Error('Este documento não pertence ao catálogo publicado.'); return response.json() as Promise<Document>; })
      .then((result) => { if (!controller.signal.aborted && result.id === requested) setDocument(result); })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Falha ao carregar o documento.'); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [preview, selected]);

  function navigate(id: string) {
    setSelected(id);
    history.pushState({}, '', id === 'landing' ? '/' : `/documents/${encodeURIComponent(id)}`);
  }

  useEffect(() => {
    const onPopState = () => setSelected(documentIdFromPath());
    addEventListener('popstate', onPopState);
    return () => removeEventListener('popstate', onPopState);
  }, []);

  if (error && !preview) return <main className="fatal" role="alert"><span className="eyebrow">PROJECT KNOWLEDGE</span><h1>Prévia indisponível</h1><p>{error}</p></main>;
  const current = preview?.documents.find((item) => item.id === selected);
  return <div className="shell">
    <aside className="sidebar">
      <a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate('landing'); }}><span className="brand-mark">B</span><span>belluga<span className="brand-sub">BUILDER WORKSPACE</span></span></a>
      <p className="nav-label">PROJECT</p>
      <button className={`nav-item ${selected === 'landing' ? 'active' : ''}`} onClick={() => navigate('landing')}><span className="nav-icon">◈</span>Visão geral</button>
      <p className="nav-label">FOUNDATION</p>
      {preview?.documents.filter((doc) => doc.id !== 'landing').map((doc) => <button key={doc.id} className={`nav-item ${selected === doc.id ? 'active' : ''}`} onClick={() => navigate(doc.id)}><span className="nav-icon">{doc.id === 'genesis' ? '◇' : '↳'}</span>{doc.title}</button>)}
      <div className="sidebar-foot"><span className="status-dot" /> SOMENTE LEITURA</div>
    </aside>
    <main className="main">
      <header className="topbar"><div className="breadcrumbs">Builder <span>/</span> {current?.title ?? 'Foundation'}</div><div className="session"><span className="session-dot" /> Prévia pública <small>SEM AUTENTICAÇÃO</small></div></header>
      {preview && <section className="integrity" aria-label="Integridade da publicação"><div><span className="eyebrow">LANDING</span><strong>v{preview.landing_version}</strong></div><div><span className="eyebrow">COMMIT PUBLICADO</span><code>{preview.commit.slice(0, 12)}</code></div><div><span className="eyebrow">INTEGRIDADE LOCAL</span><strong className={preview.local_integrity === 'matching' ? 'good' : 'warn'}>{preview.local_integrity}</strong></div><div><span className="eyebrow">VERIFICAÇÃO REMOTA</span><strong className="warn">{preview.remote_verification}</strong></div></section>}
      <div className="content">
        {loading && <p className="loading" role="status">Carregando documento…</p>}
        {error && <p className="error" role="alert">{error}</p>}
        {!loading && document && <><div className="document-meta"><span>FOUNDATION DOCUMENT</span><span>SHA FIXO · {document.commit.slice(0, 12)}</span></div><article className="markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null, a: ({ href, children, ...props }) => { const local = internalDocumentPath[href ?? '']; const safeHref = local ? `/documents/${local}` : href && /^https:\/\//i.test(href) ? href : href?.startsWith('#') ? href : undefined; return safeHref ? <a href={safeHref} rel={safeHref.startsWith('https:') ? 'noreferrer' : undefined} onClick={local ? (event) => { event.preventDefault(); navigate(local); } : undefined} {...props}>{children}</a> : <span>{children}</span>; } }}>{document.markdown}</ReactMarkdown></article></>}
        <footer className="content-foot">BUNDLE <code>{preview?.bundle_fingerprint.slice(0, 16)}</code><span> · </span>FONTE IMUTÁVEL</footer>
      </div>
    </main>
  </div>;
}

const internalDocumentPath: Record<string, string> = {
  'project_landing.md': 'landing',
  'project_mandate.md': 'mandate',
  'domain_entities.md': 'domain',
  'project_constitution.md': 'constitution',
  'system_roadmap.md': 'roadmap',
  'policies/scope_subscope_governance.md': 'scope-policy',
  'todos/active/builder-genesis.md': 'genesis',
};
