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

O pacote de publicação inclui Caddy, Viewer, Registry, Reader, MCP e o
serviço de migração, que usa a imagem do Registry. O Caddy encaminha
`https://<host>/mcp` ao MCP, preserva a URI e envia os demais caminhos,
inclusive Admin e callbacks do GitHub, ao Viewer. Somente o Caddy publica
portas. Registry, Reader, Viewer e MCP usam redes privadas do Compose.

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
   `PACED_SOURCE_SHA=<SHA PACED>`. Para `ghcr.io/belluga/builder-mcp`, use
   o contexto `mcp-app` e o alvo `runtime`. Construa e publique para
   `linux/amd64`. Registre os quatro digests retornados pelo GHCR e suas origens.
3. Distribua o [manifesto](../compose.builder.remote.yaml) e o
   [Caddyfile](../docker/caddy/Caddyfile) da mesma revisão, preservando os
   caminhos relativos. Registre os checksums, os dois SHAs, a plataforma e
   os quatro digests do GHCR. O manifesto fixa também o digest do Caddy.
   A conta de pull do servidor precisa apenas ler os pacotes privados.
   Mantenha a configuração e os segredos de cada ambiente fora do pacote.

### Configurar cada ambiente

Use um arquivo de variáveis **privado, fora do Git**, com acesso limitado ao
operador. O shell e o ambiente do runner também podem preencher as variáveis,
desde que o conteúdo não apareça nos logs. Não publique a saída expandida de
`docker compose config`.

| Entrada | Valor |
| --- | --- |
| `BUILDER_VIEWER_DIGEST`, `BUILDER_REGISTRY_DIGEST`, `BUILDER_READER_DIGEST`, `BUILDER_MCP_DIGEST` | 64 dígitos hexadecimais de cada digest aprovado, sem o prefixo `sha256:`. |
| `BUILDER_REMOTE_HOST` | Hostname público, sem esquema, porta ou barra. Define o TLS, o origin HTTPS de administração e as allowlists de Host e Origin do MCP. |
| `BUILDER_MCP_COMPANY_IDS`, `BUILDER_MCP_BEARER_TOKEN` | IDs das Companies permitidas, separados por vírgulas, e bearer exclusivo do MCP, com ao menos 32 bytes ASCII visíveis. Guarde o bearer também no gerenciador de segredos do cliente MCP. |
| `BUILDER_TUNNEL_NETWORK` | Rede externa dedicada ao conector Tunnel. Preencha ao usar o overlay opcional. |
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

No modo direto, aponte o registro DNS A para o IPv4 do servidor. Adicione
AAAA quando as portas TCP 80 e 443 também estiverem acessíveis por IPv6.
Abra essas duas portas no firewall e mantenha o DNS direto para a emissão e
renovação automática de certificados pelo Caddy. A porta 80 redireciona
para HTTPS. Preserve o volume nomeado `ingress-tls` entre recriações e
releases. Ele contém o estado de TLS e deve ter acesso restrito.

O Viewer é público. Configure o cliente MCP com `https://<host>/mcp` e
`Authorization: Bearer <token>`. Se o cliente enviar Origin, use
`https://<host>`. Operações de Project ainda exigem a sessão vinculada
ao Project correto. O Admin exige um JWT Access válido para o issuer, a
audiência e as permissões configuradas. Preserve a configuração completa
App/Access no Registry para inicializar a credencial GitHub App usada por
leituras e provas. Sem a identidade Access, use a entrada direta para
Viewer e MCP.

Para usar Cloudflare Tunnel, acrescente
`-f deploy/compose.builder.tunnel.yaml` à mesma invocação do Compose e
conecte o conector à rede externa dedicada. Aponte o hostname completo
para `https://builder-remote-ingress:443` e configure o conector para enviar
`BUILDER_REMOTE_HOST` como Host e nome TLS da origem. Mantenha a validação do
certificado e garanta o provisionamento e renovação do certificado dessa
origem antes de mudar o DNS para o Tunnel. O roteamento de `/mcp` é feito
pelo Caddy. Se usar Cloudflare Access para Admin, defina a política no
Cloudflare para entregar a identidade assinada aos caminhos administrativos
e aos callbacks. O MCP usa seu próprio bearer.

Registre no GitHub App o Setup URL `https://<host>/admin/github/callback`
e o OAuth callback `https://<host>/admin/github/oauth/callback`. Use o fluxo
de OAuth separado e tokens de usuário com expiração habilitada.

### Ensaiar antes de instalar

No CI do release, execute o smoke abaixo com Docker Compose 2.24.4 ou
superior, Bash, OpenSSL e Python 3.9 ou superior. Passe as referências por
digest publicadas no GHCR e o SHA Builder gravado no Viewer. O smoke
aceita tags locais para ensaiar imagens recém-compiladas. Ele cria uma CA
local confiada pelo cliente, um Mongo descartável, dados e credenciais
sintéticos, e usa fixtures controladas para as leituras GitHub App.

```bash
tools/ci/builder-ingress-smoke.sh "$viewer_image" "$registry_image" \
  "$reader_image" "$mcp_image" "$builder_sha"
```

Para o ensaio de instalação, use um App e Cloudflare Access de teste, um
Mongo descartável com transações e um `/state` próprio. Provisione primeiro a identidade da Company no
controle isolado para que `migrate-all` a descubra. Depois de confirmar a migração
e a prontidão do Registry, aplique ou verifique Project em modo `complete`,
repositório visual privado, dois repositórios privados de código, Release com
branches confirmadas, TODO v2 e artefato publicado. Use um Mongo separado para o ensaio. Confirme o destino do URI e as credenciais
antes da migração. O nome do banco de controle, sozinho, não isola os bancos
das Companies.

O exemplo pressupõe Docker Compose v2, login de pull e arquivo privado
prontos. Use o mesmo bloco no ensaio e no servidor com a configuração de
cada ambiente.

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

Confira as cinco imagens efetivas e suas arquiteturas após `pull`. A etapa
`registry-migrate` usa o mesmo digest e o mesmo controle Mongo do Registry.
O serviço normal depende do sucesso da migração. A migração explicitamente
executada com `run` também valida o alvo em uma nova invocação, antes de
aceitar dependências de migração eventualmente já concluídas no Compose.

O ensaio só passa se:

- O Mongo migrar e os serviços subirem. O Viewer responde `/api/build` com o
  SHA exato pelo HTTPS do Caddy e chega ao Registry, que chega ao Reader
  numa prova real. O Registry exige schema compatível para cada Company.
- No mesmo host HTTPS, inicialize o MCP com o bearer, liste as ferramentas
  e leia um TODO com sessão vinculada. Confirme rejeição sem bearer e
  sucesso de uma nova conexão HTTPS após recriar o Caddy.
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
a restauração validada da janela congelada e direcione o tráfego para a
versão restaurada. Confira o SHA e a leitura/prova aplicáveis antes de reabrir
escritas. Depois que a versão v4 aceitar escritas, preserve os dados e prepare
uma correção compatível ou uma operação de recuperação aprovada. Trocar só as
imagens ou restaurar por cima dos dados novos perde a garantia de rollback.
