import Link from '@docusaurus/Link';
import styles from './styles.module.css';

const trustPath = [
  ['Kubernetes', 'ServiceAccount'],
  ['Operator', 'Reconciled intent'],
  ['OIDC', 'Discovery + JWKS'],
  ['Azure', 'Federated identity'],
];

export default function Homepage() {
  return (
    <main className={styles.home}>
      <section className={styles.hero}>
        <div className={styles.heroCopy}>
          <p className={styles.eyebrow}>Kubernetes → OIDC → Microsoft Entra ID</p>
          <h1>Identity without a client secret.</h1>
          <p className={styles.lede}>
            Reconcile the issuer, managed identity, federated credential, and
            ServiceAccount relationship as one inspectable control plane.
          </p>
          <div className={styles.actions}>
            <Link className={styles.primaryAction} to="/getting-started/quickstart">
              Follow the quickstart
            </Link>
            <Link className={styles.secondaryAction} to="/architecture/overview">
              Read the architecture
            </Link>
          </div>
        </div>

        <div className={styles.trustMap} aria-label="Workload identity trust path">
          <div className={styles.mapHeader}>
            <span>Trust path</span>
            <span className={styles.mapStatus}>Reconciled</span>
          </div>
          <ol>
            {trustPath.map(([name, detail], index) => (
              <li key={name}>
                <span className={styles.node}>{String(index + 1).padStart(2, '0')}</span>
                <div>
                  <strong>{name}</strong>
                  <span>{detail}</span>
                </div>
              </li>
            ))}
          </ol>
        </div>
      </section>

      <section className={styles.audiences} aria-labelledby="choose-path">
        <div className={styles.sectionLead}>
          <p className={styles.eyebrow}>Two ways into the same system</p>
          <h2 id="choose-path">Choose the depth you need.</h2>
        </div>
        <div className={styles.paths}>
          <article>
            <span className={styles.pathNumber}>01</span>
            <div>
              <h3>Use an identity</h3>
              <p>
                Create a WorkloadIdentity, use its ServiceAccount, and verify
                Azure token exchange from a Pod.
              </p>
              <Link to="/guides/workload-identity">Application developer guide →</Link>
            </div>
          </article>
          <article>
            <span className={styles.pathNumber}>02</span>
            <div>
              <h3>Operate the control plane</h3>
              <p>
                Install the chart, publish the issuer, govern permissions, and
                understand deletion and recovery boundaries.
              </p>
              <Link to="/getting-started/installation">Platform operator guide →</Link>
            </div>
          </article>
        </div>
      </section>

      <section className={styles.contract} aria-labelledby="operator-contract">
        <div>
          <p className={styles.eyebrow}>The operator contract</p>
          <h2 id="operator-contract">Safe defaults. Explicit ownership.</h2>
        </div>
        <ul>
          <li>
            <strong>Retain by default</strong>
            <span>Azure resources survive deletion unless cleanup is requested.</span>
          </li>
          <li>
            <strong>One anchored scope</strong>
            <span>Subscription, resource group, and location cannot drift silently.</span>
          </li>
          <li>
            <strong>No arbitrary adoption</strong>
            <span>Ownership evidence gates every managed identity mutation.</span>
          </li>
        </ul>
      </section>

      <section className={styles.nextStep}>
        <p>New installation</p>
        <h2>Start with prerequisites, finish with a live token exchange.</h2>
        <Link to="/getting-started/installation">Install the operator →</Link>
      </section>
    </main>
  );
}
