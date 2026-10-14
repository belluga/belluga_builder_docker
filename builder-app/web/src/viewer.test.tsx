import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { Viewer } from './viewer';

const preview = { commit: 'cfe9a6e45ea3cc593c43ab7e2bd20ad04722d6fb', landing_version: '1.4', access_mode: 'public_preview', remote_verification: 'unverifiable', local_integrity: 'matching', bundle_fingerprint: '9f634142c941e75c80dbdbe78074fdf60f13467057ec9912d3ca12394c02b606', documents: [{ id: 'landing', title: 'Project Landing' }, { id: 'mandate', title: 'Project Mandate' }] };
const doc = (id: string, markdown: string) => ({ id, title: id, path: `${id}.md`, commit: preview.commit, markdown });

function jsonResponse(value: unknown) { return Promise.resolve(new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })); }

describe('Viewer', () => {
  beforeEach(() => { history.replaceState({}, '', '/'); });
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  it('shows verified source metadata and follows the exact document ID', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => url === '/api/preview' ? jsonResponse(preview) : url.endsWith('/mandate') ? jsonResponse(doc('mandate', '# Mandate page')) : jsonResponse(doc('landing', '# Builder Landing\n\n[Mandate](project_mandate.md)'))));
    render(<Viewer />);
    expect(await screen.findByRole('heading', { name: 'Builder Landing' })).toBeInTheDocument();
    expect(screen.getByText('unverifiable')).toBeInTheDocument();
    const mandateLink = screen.getByRole('link', { name: 'Mandate' });
    expect(mandateLink).toHaveAttribute('href', '/documents/mandate');
    fireEvent.click(mandateLink);
    expect(await screen.findByRole('heading', { name: 'Mandate page' })).toBeInTheDocument();
  });

  it('renders Markdown as inert content and suppresses remote images and active URL schemes', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => url === '/api/preview' ? jsonResponse(preview) : jsonResponse(doc('landing', '# Safe\n\n| Field | State |\n| --- | --- |\n| Local | matching |\n\n[Run](javascript:alert(1)) ![tracking](https://evil.example/pixel.png)'))));
    render(<Viewer />);
    expect(await screen.findByRole('heading', { name: 'Safe' })).toBeInTheDocument();
    expect(screen.getByRole('table')).toBeInTheDocument();
    expect(document.querySelector('img')).toBeNull();
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(screen.getByText('Run').tagName).toBe('SPAN');
  });

  it('keeps the newest selection when an older request resolves afterward', async () => {
    let resolveOld!: (response: Response) => void;
    const fetchMock = vi.fn((url: string) => {
      if (url === '/api/preview') return jsonResponse(preview);
      if (url.endsWith('/landing')) return new Promise<Response>((resolve) => { resolveOld = resolve; });
      return jsonResponse(doc('mandate', '# Newest document'));
    });
    vi.stubGlobal('fetch', fetchMock);
    history.replaceState({}, '', '/documents/landing');
    render(<Viewer />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/preview/documents/landing', expect.any(Object)));
    fireEvent.click(await screen.findByRole('button', { name: /Project Mandate/ }));
    expect(await screen.findByRole('heading', { name: 'Newest document' })).toBeInTheDocument();
    await act(async () => { resolveOld(new Response(JSON.stringify(doc('landing', '# Old response')), { status: 200 })); });
    expect(screen.queryByRole('heading', { name: 'Old response' })).toBeNull();
    expect(screen.getByRole('heading', { name: 'Newest document' })).toBeInTheDocument();
  });

  it('aborts outstanding requests when the viewer unmounts', async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal('fetch', vi.fn((url: string, options?: RequestInit) => {
      if (url === '/api/preview') return jsonResponse(preview);
      signal = options?.signal as AbortSignal;
      return new Promise<Response>(() => {});
    }));
    const { unmount } = render(<Viewer />);
    await waitFor(() => expect(signal).toBeDefined());
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('presents API failures to the reader', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('{}', { status: 500 }))));
    render(<Viewer />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Não foi possível carregar o catálogo.');
  });

  it('shows an error instead of crashing on a malformed document URL', async () => {
    history.replaceState({}, '', '/documents/%E0');
    vi.stubGlobal('fetch', vi.fn((url: string) => url === '/api/preview'
      ? jsonResponse(preview)
      : Promise.resolve(new Response('{}', { status: 404 }))));
    render(<Viewer />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Este documento não pertence ao catálogo publicado.');
  });

  it('keeps the viewer usable when browser history selects a malformed document URL', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => url === '/api/preview'
      ? jsonResponse(preview)
      : url.endsWith('/landing') ? jsonResponse(doc('landing', '# Landing'))
        : Promise.resolve(new Response('{}', { status: 404 }))));
    render(<Viewer />);
    expect(await screen.findByRole('heading', { name: 'Landing' })).toBeInTheDocument();
    history.pushState({}, '', '/documents/%E0');
    fireEvent.popState(window);
    expect(await screen.findByRole('alert')).toHaveTextContent('Este documento não pertence ao catálogo publicado.');
  });
});
