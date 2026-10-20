# crew

**Os agentes de IA do seu time, sabendo uns dos outros. Instalar é toda a configuração.**

[Read in English](README.md)

Quando duas ou mais pessoas usam o Claude Code no mesmo repositório, o agente de uma não sabe o que o da outra está
fazendo. O crew deixa cada agente ver quem está fazendo o quê, quais arquivos estão reservados, quais contratos
(esquemas, migrations, tipos, dependências, APIs) acabaram de mudar e quais perguntas precisam de uma pessoa — quase em
tempo real, **sem conta, sem servidor, sem token e sem arquivo de configuração**.

```text
crew: sala de org/app — Ana#7f3c (active), Bruno#19ad (2 h ago). Cifrado via relays Nostr públicos.
```

## Instalar (uma vez por computador)

Rode em qualquer terminal (PowerShell, Terminal, bash):

```bash
claude plugin marketplace add Tecnologia-Porto-Real/crew
```

```bash
claude plugin install crew@crew
```

Pronto. Abra uma sessão **nova** do Claude Code dentro de um repositório git (no terminal ou no app). Quando um colega
fizer o mesmo no clone dele do mesmo repositório, vocês se veem. Ninguém troca link, código ou chave.

Requisitos: `git` instalado, um clone normal (não `--depth 1`) com remoto, Claude Code (testado na 2.1.258+;
recomendado 2.1.290+). Windows, macOS e Linux. Mais nada — sem Node, Python ou Docker.

Quer ligar só em um repositório? Rode os dois comandos dentro dele com `--scope local`.

## Como usar

### O que acontece sozinho

| Quando | O que você vê |
|---|---|
| Você abre uma sessão | Uma linha dizendo quem mais está na sala e no que está. O Claude recebe os detalhes (tarefas, arquivos reservados, mensagens não lidas). |
| O Claude edita um arquivo que um colega está editando | O Claude é avisado, para tomar cuidado ou te perguntar. |
| O Claude mexe num arquivo que um colega **reservou com exclusividade** | O Claude Code pede **a sua** aprovação antes (também para comandos de terminal que escrevem, apagam ou geram migrations ali). |
| O Claude edita um **arquivo de contrato** (migration, `*.sql`, esquema, tipos gerados, `package.json`/lock, Dockerfile…) | Ele fica reservado para você por 2 h automaticamente, e o Claude é lembrado de avisar o time do que mudou. |
| Você dá `git push` | Os colegas recebem um aviso curto: commits e arquivos de contrato alterados. |
| Um colega faz uma pergunta, manda um aviso que toca seus arquivos ou fala direto com você — e sua sessão está parada | A sessão acorda sozinha e o Claude mostra (a resposta começa com 📨). No máximo 4 vezes por hora. |
| O resto (recados, troca de tarefa) | Espera a sua próxima mensagem — sem gastar tokens. |

### O que dizer ao Claude

É só falar normalmente; o Claude tem as ferramentas do crew.

- "O que o time está fazendo?"
- "Avisa o time que estou na página de checkout."
- "Reserva `supabase/migrations` com exclusividade pelas próximas 2 horas, vou mudar o esquema."
- "Avisa o time que renomeei `users.email` para `users.login` e regenerei os tipos."
- "Pergunta ao time se a gente apaga a tabela antiga de pagamentos."
- "Diz pra Ana#7f3c que a API está pronta."
- "Libera minhas reservas."

### Comandos

| Comando | O que faz |
|---|---|
| `/crew:board` | Mostra o quadro do time: quem está ativo, tarefas, arquivos reservados, mensagens recentes |
| `/crew:off` | Desliga o crew neste repositório, neste computador |
| `/crew:on` | Liga de novo |
| `/crew:passphrase` | Mostra o comando de terminal para definir uma senha do time, para o crew funcionar num repositório **público** (lá ele vem desligado). A senha é digitada no seu terminal, nunca no chat |
| `/crew:new-key` | Cria `.claude/crew.key` (faça commit) para quem perdeu acesso ao repo deixar de ver as mensagens novas |

Seu nome vem do `git config user.name` (primeira palavra) mais 6 caracteres da chave do seu aparelho, ex.: `Ana#7f3c1a`.
O código muda toda semana (as chaves de aparelho giram para ninguém seguir um aparelho ao longo do tempo). Para mudar o
nome: `git config user.name "Ana"`.

## Atualizar e desinstalar

Plugins de marketplaces de terceiros não se atualizam sozinhos por padrão. Para atualizar:

```bash
claude plugin marketplace update crew
```

```bash
claude plugin update crew@crew
```

Depois abra uma sessão nova. (Ou ligue a atualização automática do marketplace `crew` em `/plugin`, no terminal.)

Para remover:

```bash
claude plugin uninstall crew@crew
```

## Problemas comuns

| Sintoma | Causa provável e solução |
|---|---|
| "no teammates yet", mas o colega instalou | Os dois precisam usar o mesmo repositório (`dono/repo` do remoto). Fork é outra sala. Clone raso fica desligado — rode `git fetch --unshallow`. Se o repo foi renomeado: `git remote set-url origin <url nova>`. |
| "off here — public repository" | Esperado: em repo público qualquer um leria a sala. Um repo que já foi público continua tratado como público. Use `/crew:passphrase` com uma senha combinada fora do repo. |
| "could not confirm that this repository is private" | No GitHub/GitLab/Bitbucket/Codeberg/Azure, o crew só liga depois que uma checagem confirma que o repo é privado (falha "fechado"). Normalmente é proxy ou falta de internet; ele tenta de novo na próxima sessão. |
| "Could not reach the relays" | Sem internet, ou um firewall bloqueia WebSocket para relays públicos. Fixe um relay que funcione em `.claude/crew.json` (abaixo). |
| Não aparece nada | Confira se abriu uma sessão **nova** depois de instalar, dentro de um repositório git com remoto, e se `CREW_OFF`, `CI` ou `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` não estão definidos. |

## Como funciona

- **O repositório é a sala.** Todo clone completo tem o mesmo primeiro commit e o mesmo caminho `dono/repo`. O crew
  deriva deles a sala e uma chave de cifragem (HKDF). Mesmo repo → mesma sala, sem precisar compartilhar nada.
- **Relays públicos carregam as mensagens, cifradas.** Os eventos vão para 3 relays [Nostr](https://nostr.com)
  públicos e gratuitos (escolhidos por hash, então o time inteiro escolhe os mesmos). O conteúdo é cifrado com
  AES-256-GCM, preenchido e assinado com uma chave por aparelho. Os relays veem uma etiqueta opaca da sala, seu IP e
  horários — nunca o conteúdo. O estado expira em 24 h; as mensagens, em 7 dias.
- **O Claude recebe tudo por hooks**: resumo ao abrir a sessão, checagem antes de edições e comandos, aviso no
  `git push` e um vigia nas sessões paradas que só acorda o Claude para o que importa.
- Ferramentas do Claude: `crew_board`, `crew_post` (task, notice, question, note, done), `crew_reserve`, `crew_settings`.

## Privacidade e segurança — leia

- **Quem lê as mensagens do time:** quem pode ler o repositório, hoje ou no passado (a chave vem do histórico) — as
  mesmas pessoas que já leem seu código. Para cortar quem perdeu acesso, rode `/crew:new-key` e faça commit de
  `.claude/crew.key`.
- **Repositórios públicos vêm desligados**, e a checagem falha "fechado": nos serviços públicos (GitHub etc.) o crew
  só liga depois que o servidor confirma que o repositório é privado, e um repositório que já foi público continua
  desligado (a não ser com senha do time). **Clones rasos, repos sem remoto e CI também ficam desligados.**
- **Forks ou espelhos privados de um projeto público** herdam o primeiro commit dele, então a chave depende só do nome
  `dono/repo`, que dá para adivinhar. Nesse caso rode `/crew:new-key` uma vez e faça commit de `.claude/crew.key`.
- **Sem sigilo futuro:** quem ganhar acesso de leitura ao repo depois consegue decifrar mensagens que os relays ainda
  guardem (eles são instruídos a apagar em 7 dias, mas isso depende de cada relay).
- Mensagens têm até 500 caracteres, formatos comuns de segredo são removidos, conteúdo de `.env` é recusado e o texto
  que chega é mostrado ao Claude como informação de um colega — nunca como instrução.
- Os operadores dos relays veem metadados (IP, horário, tamanho aproximado, quantos aparelhos falam numa sala). Se sua
  empresa proíbe isso, defina `CREW_OFF=1` (ou `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`) ou fixe um relay próprio.
  A etiqueta pública da sala e as chaves de aparelho mudam toda semana, mas o tráfego (eventos 4848/34848) é
  reconhecível como sendo do crew.
- **Colegas não conseguem fazer o Claude agir por eles:** o texto deles é limpo (sem tags, quebras de linha, caracteres
  de controle ou invisíveis), limitado, identificado como vindo de um colega e nunca dispara ações; reservas
  exclusivas só fazem o Claude Code perguntar a *você*. Uma reserva do repositório inteiro é ignorada.
- **Executáveis verificáveis:** os executáveis em `plugin/runtime/` são compilados de forma reprodutível; a CI os
  recompila a partir do código a cada push e falha se forem diferentes. As impressões digitais estão em
  `plugin/runtime/SHA256SUMS`.

## Opções do time (opcional, commitado no repo)

`.claude/crew.json`:

```json
{ "relays": ["wss://seu-relay"], "contract": ["api/schema/**", "shared/types.ts"] }
```

`relays` substitui os relays públicos neste repositório (só `wss://`, até 5; o crew avisa todo mundo quando um repo
fixa relays); `contract` acrescenta seus próprios arquivos de contrato.

## Licença

MIT
