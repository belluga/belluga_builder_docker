# Boilerplate Flutter & Laravel com Docker

Este repositório agora usa a topologia root/docker vNext extraída do Belluga
Now, mas sem manter identidade Belluga hardcoded na base genérica.

## O que a base espera

- `laravel-app/`: código do backend Laravel.
- `web-app/`: shell web estático já publicado para servir via NGINX.
- `project/`: overlays downstream-owned (`nginx/`, `laravel/`, `well-known/`).

O container root não compila mais o frontend dentro do `docker compose`. A base
assume que o projeto downstream já entrega o shell web em `web-app/`.

## Topologia

- `app`, `worker` e `scheduler` compartilham o mesmo runtime Laravel.
- `nginx` serve o backend PHP e o shell web estático.
- `mongo` + `mongo-init` são opcionais via profile `local-db`.
- `cloudflared` é opcional via profile `local-tunnel`.
- `certbot` continua restrito ao profile `production`.

## Setup rápido

1. Copie o ambiente base:

```bash
cp .env.example .env
```

2. Ajuste pelo menos `PROJECT_NAME`, `PROJECT_PREFIX`, `DOMAIN` e `CERTBOT_EMAIL`.

3. Garanta que o downstream já forneça `laravel-app/` e `web-app/`.

Na lane atual deste repositório, `laravel-app/` e `web-app/` locais são as
sources of truth dos artefatos de aplicação e de CI local.

4. Se precisar de túnel local, crie o arquivo local de segredo:

```bash
cp .env.local.tunnel.example .env.local.tunnel
```

`CLOUDFLARE_TUNNEL_TOKEN` fica apenas em `.env.local.tunnel`, não em `.env.example`.

### Domínio landlord e hosts tenant

`DOMAIN` e `laravel-app/.env:APP_URL` representam o mesmo domínio raiz do
landlord, por exemplo `yourdomain.com` e `https://yourdomain.com`.
`APP_URL` não deve receber o hostname de um tenant.

O host de um tenant é resolvido separadamente pelo registro no banco:

- `Tenant.subdomain=platform-test` resulta em
  `platform-test.yourdomain.com` quando o landlord raiz é
  `yourdomain.com`.
- Um domínio customizado precisa estar cadastrado em
  `landlord.domains.path` para o tenant correspondente.

Portanto, DNS wildcard ou ingress do túnel apenas tornam o host alcançável;
eles não criam o vínculo do tenant no banco.

### Configuracao mobile neutra

O boilerplate nao exige configuracao real de Android ou iOS, como IDs de
aplicativo, URLs de loja ou publicacao mobile. Sem uma politica mobile fornecida
pelo downstream, o endpoint generico `/open-app` preserva o alvo web original.

Uma aplicacao downstream pode publicar sua propria politica e fallback de
promocao, mas essa configuracao nao deve ser criada artificialmente no banco
ou tratada como requisito do runner generico.

## Execução local

Com Mongo local:

```bash
APP_ENV=local COMPOSE_PROFILES=local-db docker compose up -d --build
```

Sem Mongo local:

```bash
APP_ENV=local COMPOSE_PROFILES= docker compose up -d --build
```

Com túnel local:

```bash
APP_ENV=local COMPOSE_PROFILES=local-db,local-tunnel \
docker compose --env-file .env --env-file .env.local.tunnel up -d --build
```

Comandos úteis:

```bash
docker compose ps
docker compose logs -f --tail=200
docker compose exec app php artisan <comando>
docker compose exec app composer install
```

## Contratos de CI locais

O runner root de contratos fica em:

```bash
bash tools/ci/run_contract.sh --list --profile stage-full
```

Superfícies atuais:

- `stage-full`: broadest local CI Equivalent at the current root/docker boundary; ele agrega invariantes root-owned e preflight de imagens runtime, sem alegar paridade de successor fronts web/flutter/browser.
- `stage-full` falha fechado se o `laravel-app/` local ou o `web-app/` local não estiverem materializados antes do `compose config`.
- `main-proof`: separate production-lane semantic guard; após o cutover final ele preserva o mesmo corpo positivo root-owned de `stage-full`, mas continua sendo a surface explícita de prova para a lane de promoção.

Execução:

```bash
bash tools/ci/run_contract.sh --profile stage-full
bash tools/ci/run_contract.sh --profile main-proof
```

## Produção

```bash
APP_ENV=production COMPOSE_PROFILES=production docker compose up -d --build
```

`DOMAIN` e `CERTBOT_EMAIL` devem apontar para o domínio real de produção.

## Overlays downstream-owned

Use `project/` para manter identidade específica fora da base compartilhada:

- `project/nginx/routes.conf.example`: famílias extras de rota antes do fallback SPA.
  As rotas públicas gerenciadas devem incluir `# public_shell_route_id: <id>`
  dentro de cada `location` para que o harness de paridade compare NGINX e
  Laravel pelo mesmo inventário. Endpoints e famílias root-owned como
  `/open-app`, `/manifest.json` e `/icon/*` ficam fora do overlay do projeto.
- `project/laravel/public_shell_routes.example.php`: inventário das rotas
  públicas project-owned consumido pelo allowlist genérico do Laravel. O
  exemplo de `custom_public_metadata` usa de propósito um
  `service_container_id` placeholder com prefixo `project.*`; o downstream
  deve substituir ou bindar esse ID para um serviço próprio que implemente o
  contrato de extensão de metadata antes de ativar a rota de exemplo.
- `project/laravel/required_runtime_classes.example.txt`: classes críticas para a
  verificação de autoload no entrypoint.
- `project/well-known/*.example.json`: exemplos de payload para App Links /
  Universal Links.

Se `project/nginx/` estiver vazio, o `include /etc/nginx/project/*.conf` é no-op.

## Cutover Final

O seam temporário de validação específico do projeto downstream foi retirado da
base root/docker. A prova local autoritativa agora depende apenas dos
inputs locais `laravel-app/` e `web-app/`, mais as contracts root-owned em
`tools/ci/contracts/**`.

## Builder no servidor dedicado

O pacote da Release Conexão GitHub fica em [compose.builder.remote.yaml](compose.builder.remote.yaml).
O [runbook de publicação do Builder](builder-app/README.md#publicação-remota-do-builder)
descreve o ensaio obrigatório com as mesmas imagens por digest, a migração do
MongoDB e a verificação no servidor. O pacote de runtime usa Docker Compose e
um Cloudflare Tunnel autorizado. Não exige checkout ou compilação no servidor.
