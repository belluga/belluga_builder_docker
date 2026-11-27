# Builder Project Knowledge preview

The MVP serves the registered Company/Project catalog anonymously and read-only. It provides no IAM, MCP or editing. Only admitted, fingerprint-verified snapshot bytes are served. Loading a Project asks the private Delphi reader for the current published Foundation revision; registration binds identity and source, but does not publish content. Old path-only bindings are invalid and must be registered again.

From the Builder Docker root:

The local preview launcher creates its internal Builder-to-reader bearer token and uses the host's existing `gh` login for private Foundation reads. The Builder consumer receives only the internal bearer, never the Git credential; neither reaches the browser. An operator may supply `DELPHI_PROJECT_READER_TOKEN` or `DELPHI_GITHUB_READ_TOKEN` explicitly, but neither is required for the local preview when `gh` is already authenticated. Use of the host `gh` session, even if it has broader permissions, is an owner-approved temporary MVP exception for this local reader; the reader issues Git reads only. The provisional API remains local; remote service hosting and IAM are separate future work.

```bash
docker build --target go-test -f builder-app/Dockerfile builder-app
docker build --target web-test -f builder-app/Dockerfile builder-app
docker build --target runtime -f builder-app/Dockerfile builder-app
DELPHI_PROJECT_READER_TOKEN="$(openssl rand -hex 32)" \
DELPHI_GITHUB_READ_TOKEN="$(gh auth token)" \
DELPHI_BUILDER_STATE_ROOT=/home/elton/Dev/builder-local-state \
BUILDER_UID=$(id -u) BUILDER_GID=$(id -g) \
BUILDER_SOURCE_SHA=$(git rev-parse HEAD) \
PACED_SOURCE_SHA=$(git -C delphi-ai rev-parse HEAD) \
docker compose -p builder-mvp -f compose.builder.yaml up --build -d
```

The `go-test` stage installs Git for its Git-backed integration test fixtures; Git is not included in the final Builder runtime image. The private Delphi reader image contains Git for its published-source reads.

Run that Compose command from the principal checkout on `mvp` after confirming its HEAD equals remote `mvp`, and with the PACED checkout at a committed, pushed `builder` revision. A `git fetch` alone does not advance the checked-out branch. The public `GET /api/build` returns the Builder source SHA embedded during the build; MVP completion proof rejects an absent or mismatched SHA. The private Reader likewise embeds its PACED source SHA and verifies claimed PACED commits. The public UI is at `https://builder.bellugademo.com.br/` (or `http://127.0.0.1:8088/` on the host). The Builder runtime mounts shared derived state read-only; it does not mount a Foundation checkout or `.git`. On Project load, the private reader resolves the published Foundation head and refreshes the derived snapshot if needed, without another registration. A code change still requires `mvp` promotion and principal-checkout fast-forward.

For the shared tunnel, use the existing external Docker network `builder-preview-ingress` and keep its connector attached while preserving its default network. In Cloudflare's tunnel hostname configuration, point **only** `builder.bellugademo.com.br` to `http://builder-preview:8080`. Do not point a public route at the host-loopback port or expose `8088` on a non-loopback address.

After deployment, verify the anonymous Company → Project → Overview/Roadmap journey, direct links, artifact isolation and negative paths through the hostname, then smoke the pre-existing tunnel hostnames. To stop only the MVP runtime, run `docker compose -p builder-mvp -f compose.builder.yaml down` from the principal checkout on `mvp`.

## Local authoring preview

The development registered-Project viewer remains loopback-only at `http://127.0.0.1:8089/`. The separate MVP service publishes the same registered catalog at the approved domain. Both consumers mount the explicitly selected shared registration state directory read-only; neither mounts Project/Company/Foundation checkouts, `.git`, or environment files.

For this Builder workspace, the operator-selected shared state directory is `/home/elton/Dev/builder-local-state` (private, mode 0700, outside Project repositories). Set `DELPHI_BUILDER_STATE_ROOT` to that directory, then run `sh builder-app/service/preview-artifacts.sh` from this root. The script generates its internal bearer and uses the existing host `gh` login; it does not register Projects. This path is local operator context, not a Project input or shared Delphi default. The public Delphi launcher resolves Project/Foundation bindings and private storage internally; a Project caller supplies its identity, not filesystem or state paths. See the shared `register-builder-project` skill and `delphi-ai/local-api/contract.md`. Initialization does not register Projects automatically. The Builder consumer verifies fingerprinted bytes; the private reader resolves current source freshness on Project load and visible-view checks. The saved Knowledge status is a document-review observation, not evidence that the Landing visual artifact matches the current source.

Only files listed as available in a verified immutable snapshot are served, from fingerprint-scoped routes so relative assets remain inside that same snapshot. HTML receives a response CSP sandbox without `allow-same-origin`; Prototype HTML alone receives `allow-scripts` within the response sandbox, and the iframe remains separately sandboxed. Integrity, saved freshness observations, and human review are presented as distinct facts. The viewer is local rendering only, not publication, review, visual-fidelity approval, or an AI capability claim.

## Publicação remota do Builder

Esta seção descreve a topologia da Release Conexão GitHub. As instruções acima
se referem ao preview local e ao MVP anterior. O runtime remoto inclui Viewer,
Registry, Reader e o serviço curto de migração, que usa a mesma imagem do
Registry. O aceite desta Release cobre a administração de conexões, leitura
de artefatos privados e provas de código em modo `complete`. MCP remoto,
prova de deploy MVP, publicação de anexos e mutações do Design System são
fluxos separados.

### Produzir o pacote uma vez

1. Em um executor Linux amd64 limpo, obtenha os **dois commits aprovados**:
   Builder para Viewer e Registry, PACED para Reader. Rode as suítes Go, o
   build e testes web e a suíte Go do Reader nesses commits. Não copie um
   diretório `delphi-ai` da estação. O checkout PACED é entrada explícita do
   build e seu SHA é registrado separadamente.
2. Publique com a conta autorizada de `belluga` no GHCR. Use o contexto
   `builder-app` e `BUILDER_SOURCE_SHA=<SHA Builder>` para
   `ghcr.io/belluga/builder-viewer`. Use o root Builder e
   `-f go-app/Dockerfile --target runtime` para
   `ghcr.io/belluga/builder-registry`. Para
   `ghcr.io/belluga/builder-project-reader`, use o contexto
   `<checkout PACED>/local-api`, seu `Dockerfile` e
   `PACED_SOURCE_SHA=<SHA PACED>`. Construa e publique para `linux/amd64`.
   As três imagens são privadas. Ao terminar cada publicação, registre o
   digest retornado pelo registry e sua origem.
3. Publique junto o [manifesto de runtime](../compose.builder.remote.yaml)
   da mesma revisão. Guarde o checksum do arquivo e o registro com os dois SHAs,
   os três digests e a plataforma. O servidor e o ensaio recebem esse
   manifesto e os mesmos digests. A conta de pull do servidor precisa apenas
   ler os pacotes privados. Cópias com a configuração de uma máquina ou
   segredos preenchidos não fazem parte do pacote.

### Configurar cada ambiente

Use um arquivo de variáveis **privado, fora do Git**, com acesso limitado ao
operador. O shell e o ambiente do runner também podem preencher as variáveis,
desde que o conteúdo não apareça nos logs. Não publique a saída expandida de
`docker compose config`.

| Entrada | Valor |
| --- | --- |
| `BUILDER_VIEWER_DIGEST`, `BUILDER_REGISTRY_DIGEST`, `BUILDER_READER_DIGEST` | 64 dígitos hexadecimais de cada digest aprovado, sem o prefixo `sha256:`. |
| `BUILDER_REMOTE_HOST` | Hostname protegido, sem esquema, porta ou barra. Reutilizado no origin HTTPS de administração. |
| `BUILDER_TUNNEL_NETWORK` | Rede externa dedicada ao conector Tunnel e ao Viewer. |
| `REGISTRY_MONGODB_URI`, `REGISTRY_CONTROL_DATABASE` | URI de réplica MongoDB ou Atlas com transações e controle isolado para este ambiente. Forneça acesso aos bancos Company necessários. |
| `DELPHI_PROJECT_READER_TOKEN`, `DELPHI_PROJECT_READER_SCOPED_PROOF_TOKEN` | Dois valores distintos, cada um com no mínimo 32 bytes. Somente Registry e Reader os recebem. |
| `BUILDER_READER_STATE_PATH` | Caminho absoluto para diretório já criado no host, sem symlinks, proprietário `65532:65532`, modo `0700`. É independente da estação de desenvolvimento. |
| `BUILDER_GITHUB_APP_ID`, `BUILDER_GITHUB_APP_SLUG`, `BUILDER_GITHUB_APP_CLIENT_ID`, `BUILDER_GITHUB_APP_CLIENT_SECRET` | Identidade e configuração do App do ambiente. |
| `BUILDER_GITHUB_APP_KEY_PATH` | Caminho absoluto do PEM no host, montado somente para leitura no Registry. Diretórios privados e arquivo legível pelo UID real do Registry. |
| `BUILDER_GITHUB_ACCESS_ISSUER`, `BUILDER_GITHUB_ACCESS_AUDIENCE` | Issuer do time Cloudflare e audiência exata da aplicação Access desse host. |
| `BUILDER_GITHUB_ACCESS_OPERATORS` | Objeto JSON de `sub` verificado do Access para lista de Companies autorizadas, por exemplo `{"subject-exemplo":["company-exemplo"]}`. Use `{}` quando houver somente criadores de Company configurados. |
| `BUILDER_ADMIN_SHARED_AUTHENTICATED_ACCESS` | O Compose remoto habilita a criação de Companies e Projects para todos os usuários autenticados pelo Cloudflare Access. Use `false` para voltar às permissões configuradas. A autorização GitHub continua individual. |
| `BUILDER_ADMIN_COMPANY_CREATORS` | Lista JSON de `sub` verificado do Access que pode criar Companies, por exemplo `["subject-exemplo"]`. Com o modo compartilhado desabilitado, o padrão `[]` desabilita a criação. O criador de uma Company pode cadastrar Projects nela. Cada Project registra o criador com acesso ao seu vínculo GitHub. |

Confirme DNS, TLS, acesso ao GHCR, conectividade de saída com GitHub e Atlas ou
Mongo, capacidade de disco/memória e o controle Mongo selecionado. Descubra
o UID/GID do Registry na **imagem por digest já baixada** com
`docker run --rm --entrypoint id ghcr.io/belluga/builder-registry@sha256:<digest>`.
Ao provisionar o PEM no host, conceda leitura a esse UID efetivo, considerando
eventual remapeamento do Docker. Não faça o PEM legível para todos os usuários.
O cache Git fica em volume nomeado e começa vazio no ensaio. O Viewer usa
Registry por rede e não precisa montar `/state`.

No Cloudflare, proteja **todo o hostname** com Access e conecte o processo
Tunnel desse ambiente à rede dedicada. A origem do hostname é
`http://builder-remote-viewer:8080`. O manifesto não publica portas no host.
Registry e Reader usam outra rede e não devem ser registrados no Tunnel.
Confirme que uma sessão sem autorização não alcança nenhuma página do hostname.
Registre no GitHub App o Setup URL `https://<host>/admin/github/callback` e o
OAuth callback `https://<host>/admin/github/oauth/callback`. Use Setup URL com
o fluxo de OAuth separado e os tokens de usuário com expiração habilitada.
Não use PAT de desenvolvimento ou credencial de deploy como substituto do App
nos fluxos incluídos.

### Ensaiar antes de instalar

Num runner Docker externo e limpo, use um App e Cloudflare Access de teste,
um Mongo descartável com transações, `/state` próprio e redes/volumes novos.
Use apenas a fixture aprovada. Provisione primeiro a identidade da Company no
controle isolado para que `migrate-all` a descubra. Depois de confirmar a migração
e a prontidão do Registry, aplique ou verifique Project em modo `complete`,
repositório visual privado, dois repositórios privados de código, Release com
branches confirmadas, TODO v2 e artefato publicado. Use um Mongo separado para o ensaio. Confirme o destino do URI e as credenciais
antes da migração. O nome do banco de controle, sozinho, não isola os bancos
das Companies.

O exemplo pressupõe Docker Compose v2, login de pull já configurado, Tunnel e
arquivo privado prontos. Use o mesmo bloco nos dois ambientes, mudando apenas
seus valores e o nome de projeto.

```bash
(
set -e
builder_compose=(docker compose --project-name builder-release-ensaio --env-file /srv/builder/runtime.env -f compose.builder.remote.yaml)
test "$(uname -m)" = x86_64
"${builder_compose[@]}" config --quiet
"${builder_compose[@]}" pull
"${builder_compose[@]}" run --rm --no-deps registry-migrate
"${builder_compose[@]}" up --no-build --pull never -d
"${builder_compose[@]}" ps
)
```

Confira as três imagens efetivas e suas arquiteturas após `pull`. A etapa
`registry-migrate` usa o mesmo digest e o mesmo controle Mongo do Registry.
O serviço normal depende do sucesso da migração. A migração explicitamente
executada com `run` também valida o alvo em uma nova invocação, antes de
aceitar dependências de migração eventualmente já concluídas no Compose.

O ensaio só passa se:

- O Mongo migrar e as três aplicações subirem. O Viewer autenticado responde
  `/api/build` com o SHA exato e chega ao Registry, que chega ao Reader numa
  prova real. O Registry não aceita Companies com schema incompatível.
- A administração nega acesso sem credencial e valida Company e Origin.
  Com o operador autorizado, abra o Project no Viewer e selecione
  **Gerenciar conexão GitHub**. Exercite vínculo, leitura privada com cache
  vazio, criação e retomada conforme o TODO.
- Para o TODO v2, use o Registry privado e a ferramenta de operação autorizada
  do ambiente. Rode as transições governadas de `In-Progress` a `Completed`
  no modo `complete` com dois repositórios e as branches correspondentes.
  O executável já vem na imagem do Reader e usa a URL privada passada pelo
  manifesto. Rode `docker compose exec -T project-reader builder-project read-todo
  --company-id <company> --project-id <project> --todo-id <todo>` com os mesmos
  parâmetros de projeto e manifesto do bloco acima. Use o mesmo executável
  para `transition`, com revisões relidas, payloads de aceite v2 e operação
  estável por transição. Confirme os SHAs e as provas na revisão final.
- Reexecute leitura e prova depois de reiniciar os serviços e confirme que o
  vínculo persiste. Verifique a negação após revogação, inclusive leitura
  condicional com cache prévio, e o funcionamento dos repositórios independentes.
- Registre resultado, manifesto, digests, ambos os SHAs e evidência da journey
  no runner. Uma falha ou troca de manifesto, plataforma ou digest exige
  novo ensaio antes da instalação.

### Instalar a versão aprovada e voltar se necessário

Antes da migração no servidor dedicado, registre o pacote anterior e os vínculos
de armazenamento e prepare um Mongo restaurável. Prefira migrar controle e
dados isolados mantendo a versão anterior intacta, ou use backup com teste de
restauração dentro de uma janela sem novas escritas. Use um nome de projeto
próprio, a configuração real e o mesmo arquivo/digests aprovados pelo ensaio.
Repita o preflight e o bloco Compose. Valide o hostname, DNS, TLS, Access,
Mongo e GitHub App reais com a máquina de desenvolvimento fora do fluxo.
Registre a fase e o resultado com a evidência remota.

Uma versão anterior do Registry com catálogo de schema v3 rejeita uma base
marcada v4. Se for necessário voltar **antes de admitir novas escritas**,
retome o pacote anterior por digest **junto com** o banco anterior intacto ou
a restauração validada da janela congelada, apontando o Tunnel somente para
essa versão. Confira o SHA e a leitura/prova aplicáveis antes de reabrir
escritas. Depois que a versão v4 aceitar escritas, preserve os dados e prepare
uma correção compatível ou uma operação de recuperação aprovada. Trocar só as
imagens ou restaurar por cima dos dados novos perde a garantia de rollback.
