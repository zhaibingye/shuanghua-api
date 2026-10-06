<div align="center">

![new-api](/web/public/logo.png)

# New API

**Une passerelle IA pour les modèles, les applications et les agents**

<p align="center">
  <a href="./README.zh_CN.md">简体中文</a> |
  <a href="./README.zh_TW.md">繁體中文</a> |
  <a href="./README.md">English</a> |
  <strong>Français</strong> |
  <a href="./README.ja.md">日本語</a>
</p>

<p align="center">
  <a href="https://raw.githubusercontent.com/Calcium-Ion/new-api/main/LICENSE">
    <img src="https://img.shields.io/github/license/Calcium-Ion/new-api?color=brightgreen" alt="licence">
  </a><!--
  --><a href="https://github.com/Calcium-Ion/new-api/releases/latest">
    <img src="https://img.shields.io/github/v/release/Calcium-Ion/new-api?color=brightgreen&include_prereleases" alt="version">
  </a><!--
  --><a href="https://hub.docker.com/r/CalciumIon/new-api">
    <img src="https://img.shields.io/badge/docker-dockerHub-blue" alt="docker">
  </a>
  <a href="https://atomgit.com/QuantumNous/new-api" target="_blank">
    <img alt="AtomGit G-Star" src="https://atomgit.com/QuantumNous/new-api/star/badge.svg"/>
  </a>
</p>

<p align="center">
  <a href="https://trendshift.io/repositories/20180" target="_blank">
    <img src="https://trendshift.io/api/badge/repositories/20180" alt="QuantumNous%2Fnew-api | Trendshift" style="width: 250px; height: 55px;" width="250" height="55"/>
  </a>
  <br>
  <a href="https://hellogithub.com/repository/QuantumNous/new-api" target="_blank">
    <img src="https://api.hellogithub.com/v1/widgets/recommend.svg?rid=539ac4217e69431684ad4a0bab768811&claim_uid=tbFPfKIDHpc4TzR" alt="Featured｜HelloGitHub" style="width: 250px; height: 54px;" width="250" height="54" />
  </a><!--
  -->
  <a href="https://atomgit.com/QuantumNous/new-api" target="_blank">
    <img alt="AtomGit G-Star" src="https://atomgit.com/QuantumNous/new-api/star/new_badge.svg" width="250" height="55" />
  </a>
</p>

<p align="center">
  <a href="#capabilities">Fonctionnalités</a> •
  <a href="#quick-start">Démarrage rapide</a> •
  <a href="#deployment">Déploiement</a> •
  <a href="#development">Développement</a> •
  <a href="#documentation">Documentation</a>
</p>

</div>

---

## 📝 Description du projet

New API est une passerelle IA auto-hébergée pour les applications, les agents et les équipes. Connectez vos fournisseurs de modèles, exposez une API commune à vos clients et gérez le routage, les accès, les usages et les coûts depuis une même console.

Utilisez-la pour partager des accès autorisés au sein d'une équipe, changer de fournisseur sans reconfigurer chaque client ou exploiter un service privé multi-modèles. Les fournisseurs incluent OpenAI, Anthropic, Google Gemini, Azure OpenAI, AWS Bedrock, Vertex AI, DeepSeek, Qwen et d'autres services compatibles.

> [!IMPORTANT]
> - Ce projet est exclusivement destiné aux scénarios de passerelle API d'IA légalement autorisés, d'authentification organisationnelle, de gestion multi-modèles, d'analyse d'utilisation, de comptabilisation des coûts et de déploiement privé.
> - Les utilisateurs doivent obtenir légalement les clés API, comptes, services de modèles et autorisations d'interface en amont, et doivent respecter les conditions d'utilisation en amont et les lois et réglementations applicables.
> - Les utilisateurs doivent s'assurer que leur utilisation est conforme aux conditions d'utilisation en amont et aux lois et réglementations applicables.
> - Lors de la fourniture de services d'IA générative au public, les utilisateurs doivent se conformer aux exigences réglementaires applicables et remplir toutes les obligations d'enregistrement, de licence, de sécurité du contenu, de vérification d'identité, de conservation des journaux, de fiscalité et d'autorisation en amont requises par leur juridiction.

<!-- -->

> [!WARNING]
> Lorsque vous exploitez ce projet en tant que service public d'IA générative ou service de revente d'API, les utilisateurs doivent d'abord remplir toutes les obligations requises en matière d'enregistrement, de licence, de sécurité du contenu, de vérification d'identité, de conservation des journaux, de fiscalité, de paiement et d'autorisation en amont.

---

## 🤝 Partenaires de confiance

<p align="center">
  <em>Sans ordre particulier</em>
</p>

<p align="center">
  <a href="https://www.cherry-ai.com/" target="_blank">
    <img src="./docs/images/cherry-studio.png" alt="Cherry Studio" height="80" />
  </a><!--
  --><a href="https://github.com/iOfficeAI/AionUi/" target="_blank">
    <img src="./docs/images/aionui.png" alt="Aion UI" height="80" />
  </a><!--
  --><a href="https://bda.pku.edu.cn/" target="_blank">
    <img src="./docs/images/pku.png" alt="Université de Pékin" height="80" />
  </a><!--
  --><a href="https://www.aliyun.com/" target="_blank">
    <img src="./docs/images/aliyun.png" alt="Alibaba Cloud" height="80" />
  </a><!--
  --><a href="https://io.net/" target="_blank">
    <img src="./docs/images/io-net.png" alt="IO.NET" height="80" />
  </a>
</p>

---

## 🙏 Remerciements spéciaux

<p align="center">
  <a href="https://www.jetbrains.com/?from=new-api" target="_blank">
    <img src="https://resources.jetbrains.com/storage/products/company/brand/logos/jb_beam.png" alt="JetBrains Logo" width="120" />
  </a>
</p>

<p align="center">
  <strong>Merci à <a href="https://www.jetbrains.com/?from=new-api">JetBrains</a> pour avoir fourni une licence de développement open-source gratuite pour ce projet</strong>
</p>

---

<a id="capabilities"></a>

### Ce Fork

```bash
# Cloner ce fork
git clone https://github.com/zhaibingye/shuanghua-api.git
cd shuanghua-api

# Démarrer avec l'image fork configurée dans docker-compose.yml
docker compose up -d
```

Image Docker du fork : `yeranshuanghua/shuanghua-api:latest`

Ce fichier compose attend que PostgreSQL et Redis soient sains avant de démarrer l'application. Utilisez Docker Compose v2 (`docker compose`, pas l'ancien `docker-compose`). Journaux : `docker compose logs -f new-api`.

Si le pull de `yeranshuanghua/shuanghua-api:latest` est lent ou échoue (fréquent quand Docker Hub est limité ou non mis en cache), configurez un miroir de registre puis exécutez `docker compose pull && docker compose up -d`. Vous n'avez **pas** besoin de déployer QuantumNous/new-api d'abord.

### Utilisation de Docker Compose (recommandé)

```bash
git clone https://github.com/QuantumNous/new-api.git
cd new-api
```

Avant de démarrer, modifiez `docker-compose.yml` : remplacez les mots de passe d'exemple de la base et de Redis dans les services et les chaînes de connexion, puis définissez un `SESSION_SECRET` aléatoire et persistant (générable avec `openssl rand -hex 32`). Pour une console HTTPS, définissez `SESSION_COOKIE_SECURE=true` et renseignez son origine HTTPS publique exacte dans `SESSION_COOKIE_TRUSTED_URL`.

```bash
docker compose up -d
docker compose logs -f new-api
```

### Stockage et configuration

| Composant | Options |
| --- | --- |
| Base principale | SQLite, MySQL ≥ 5.7.8 ou PostgreSQL ≥ 9.6 |
| Base de journaux distincte | Configurée avec `LOG_SQL_DSN` ; prend aussi en charge ClickHouse |
| Cache | Redis facultatif et cache mémoire ; partagez Redis si les nœuds doivent partager les limites de débit |
| Plateformes des conteneurs | Linux amd64 / arm64 |

| Variable | Rôle |
| --- | --- |
| `SQL_DSN` | Connexion à la base principale ; SQLite si non définie |
| `LOG_SQL_DSN` | Connexion facultative à une base de journaux distincte |
| `REDIS_CONN_STRING` | Chaîne de connexion Redis |
| `SESSION_SECRET` | Secret d'authentification persistant, identique sur tous les nœuds |
| `CRYPTO_SECRET` | Vaut `SESSION_SECRET` par défaut ; même valeur effective pour les nœuds partageant Redis |
| `SESSION_COOKIE_SECURE` | `true` pour une console HTTPS ; active les cookies de renouvellement Secure et les contrôles stricts d'origine du renouvellement et de la déconnexion |
| `SESSION_COOKIE_TRUSTED_URL` | Obligatoire en mode Secure : origines HTTPS exactes séparées par des virgules, sans chemin ni joker ; à laisser non définie en HTTP local |
| `TRUSTED_PROXIES` | IP/CIDR des proxys de confiance, ou `none` ; à configurer selon votre réseau |

Consultez l'[exemple d'environnement](./.env.example), la [référence des variables](https://docs.newapi.ai/en/docs/installation/config-maintenance/environment-variables) et le [guide des sessions](./docs/authentication.md). Injectez les variables du conteneur avec `environment` ou `env_file` dans Compose ; copier `.env.example` ne suffit pas.

En production, utilisez HTTPS et configurez le proxy inverse pour le streaming et WebSocket. Conservez et sauvegardez les bases et les données montées. Tous les nœuds doivent partager la base principale et les secrets d'authentification ; des Redis distincts ou des limiteurs en mémoire comptent les limites séparément par nœud. Le guide des sessions détaille la propagation selon la topologie.

Choisissez une version précise d'image dans les [versions publiées](https://github.com/QuantumNous/new-api/releases), lisez ses notes et sauvegardez avant toute mise à niveau. Le tag `latest` évolue avec les publications ; évaluez les migrations et la compatibilité de votre installation.

<a id="development"></a>

## Développement et extensions

Le backend utilise Go et Gin ; la console utilise React 19, TypeScript, Rsbuild, TanStack et Tailwind CSS 4. Utilisez Bun pour le frontend. Consultez [go.mod](./go.mod) pour la version de référence du langage Go et [Dockerfile](./Dockerfile) pour la chaîne de compilation du conteneur.

Compilez le frontend avant de démarrer le backend, qui intègre `web/dist` :

```bash
# Racine du dépôt
cd web
bun install --frozen-lockfile
bun run build
cd ..
go run .
```

Dans un second terminal, démarrez le serveur de développement frontend :

```bash
cd web
bun run dev -- --port 5173
```

Ouvrez [http://localhost:5173](http://localhost:5173) ; les requêtes API sont relayées vers le backend sur le port 3000. Pour un backend de développement en conteneur, consultez [docker-compose.dev.yml](./docker-compose.dev.yml) et la cible `make dev` du [makefile](./makefile).

| Emplacement | Responsabilité |
| --- | --- |
| `router/`, `middleware/`, `controller/` | Routes HTTP, contrôles d'accès et gestionnaires API |
| `relay/` | Adaptateurs amont et routage des requêtes |
| `service/`, `model/` | Logique métier et persistance |
| [relaykit/](./relaykit/README.md) | Module Go autonome pour les DTO et conversions de protocoles |
| [plugins/tasks/](./plugins/tasks/) | Extensions de tâches JavaScript ; contrat et limites de l'hôte dans [Task Plugin API v1](./docs/plugin-api/v1.md) |
| `web/` | Console web ; voir les [conventions frontend](./web/AGENTS.md) |
| [electron/](./electron/README.md) | Application de bureau et packaging |

Lisez [AGENTS.md](./AGENTS.md) avant de contribuer. Exécutez les vérifications adaptées : `make test` pour les modules Go ; `bun run typecheck`, `bun run lint`, `bun run test` et `bun run build` dans `web/` pour le frontend. Une modification de RelayKit nécessite aussi `GOWORK=off go build ./...` depuis `relaykit/`.

<a id="documentation"></a>

## Documentation et communauté

| Ressource | Lien |
| --- | --- |
| Documentation officielle | [Guides](https://docs.newapi.ai/en/docs) · [Installation](https://docs.newapi.ai/en/docs/installation) · [Référence API](https://docs.newapi.ai/en/docs/api) |
| Exploration du projet | [DeepWiki](https://deepwiki.com/QuantumNous/new-api) |
| Questions et échanges | [FAQ](https://docs.newapi.ai/en/docs/support/faq) · [Communauté](https://docs.newapi.ai/en/docs/support/community-interaction) |
| Bugs et propositions | [GitHub Issues](https://github.com/QuantumNous/new-api/issues) |
| Vulnérabilités | Signalement privé selon la [politique de sécurité](./.github/SECURITY.md) |

Pour signaler un bug, indiquez la version, le déploiement, les étapes de reproduction et des journaux expurgés des données sensibles. Les contributions à la documentation, aux traductions, aux intégrations et aux tests de régression ciblés sont les bienvenues.

---

## 🔗 Projets connexes

### Projets en amont

| Projet | Description |
|------|------|
| [One API](https://github.com/songquanpeng/one-api) | Base du projet original |
| [Midjourney-Proxy](https://github.com/novicezk/midjourney-proxy) | Prise en charge de l'interface Midjourney |

### Outils d'accompagnement

| Projet | Description |
|------|------|
| [new-api-key-tool](https://github.com/Calcium-Ion/new-api-key-tool) | Outil de recherche de quota d'utilisation avec une clé |
| [new-api-horizon](https://github.com/Calcium-Ion/new-api-horizon) | Version optimisée haute performance de New API |

---

## 📜 Licence

Ce projet est sous licence [GNU Affero General Public License v3.0 (AGPLv3)](./LICENSE).

Des [conditions supplémentaires](./NOTICE) s'appliquent au titre de la section 7 de l'AGPLv3. Les versions modifiées doivent conserver la mention `Frontend design and development by New API contributors.` dans les mentions légales appropriées et les emplacements visibles de l'interface dédiés aux informations, au droit, au pied de page ou aux attributions, ainsi qu'un lien visible vers le projet original : <https://github.com/QuantumNous/new-api>.

Il s'agit d'un projet open-source développé sur la base de [One API](https://github.com/songquanpeng/one-api) (licence MIT).

Si les politiques de votre organisation ne permettent pas l'utilisation de logiciels sous licence AGPLv3, ou si vous souhaitez éviter les obligations open-source de l'AGPLv3, veuillez nous contacter à : [support@quantumnous.com](mailto:support@quantumnous.com)

Consultez [NOTICE](./NOTICE) et les [licences tierces](./THIRD-PARTY-LICENSES.md) pour les attributions et les dépendances.

---

## 🌟 Historique des étoiles

<div align="center">

[![Fork Star History Chart](https://api.star-history.com/svg?repos=zhaibingye/shuanghua-api&type=Date)](https://star-history.com/#zhaibingye/shuanghua-api&Date)

<sub>Dépôt fork : zhaibingye/shuanghua-api</sub>

[![Graphique de l'historique des étoiles](https://api.star-history.com/svg?repos=Calcium-Ion/new-api&type=Date)](https://star-history.com/#Calcium-Ion/new-api&Date)

</div>

---

<div align="center">

### 💖 Merci d'utiliser New API

Si ce projet vous est utile, bienvenue à nous donner une ⭐️ Étoile！

**[Documentation officielle](https://docs.newapi.ai/en/docs)** • **[Commentaires sur les problèmes](https://github.com/Calcium-Ion/new-api/issues)** • **[Dernière version](https://github.com/Calcium-Ion/new-api/releases)**

<sub>Construit avec ❤️ par QuantumNous</sub>

</div>
