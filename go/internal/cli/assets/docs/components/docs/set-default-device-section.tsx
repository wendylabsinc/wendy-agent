import { Callout } from 'fumadocs-ui/components/callout';

export function SetDefaultDeviceSection() {
  return (
    <section>
      <h2 id="setting-a-default-device">Setting a Default Device</h2>
      <p>
        If you have a device you use frequently, set it as your default to skip
        the selection prompt for everyday commands.
      </p>

      <h3 id="set-default-device">Set Default Device</h3>
      <p>Run the interactive default-device picker:</p>
      <pre>
        <code className="language-bash">wendy device set-default</code>
      </pre>
      <p>
        The chosen device is saved to <code>~/.wendy/config.json</code>. After
        that, commands like <code>wendy run</code> automatically use the saved
        device.
      </p>

      <h3 id="clear-default-device">Clear Default Device</h3>
      <p>Return to interactive selection:</p>
      <pre>
        <code className="language-bash">wendy device unset-default</code>
      </pre>

      <h3 id="override-default-temporarily">Override Default Temporarily</h3>
      <p>Use the <code>--device</code> flag for a single command:</p>
      <pre>
        <code className="language-bash">wendy run --device wendyos-other-device.local</code>
      </pre>

      <Callout type="info">
        <strong>Selection Priority</strong>: the CLI checks <code>--device</code>,
        then <code>WENDY_AGENT</code>, then the saved default, then interactive
        discovery.
      </Callout>
    </section>
  );
}
