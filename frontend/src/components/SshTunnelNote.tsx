// A single host publishes a project's ports on its own 127.0.0.1 (EXC-576):
// a client elsewhere forwards the same port over SSH and dials 127.0.0.1 too,
// so the strings and the gateway certificate work unchanged.
export function SshTunnelNote({ port }: { readonly port: number }) {
  return (
    <p className="text-xs text-text-tertiary" data-testid="conn-ssh-tunnel">
      From another machine, forward the port first:{' '}
      <code className="font-mono">
        ssh -N -L {port}:127.0.0.1:{port} you@your-server
      </code>
      , then use the same string there.
    </p>
  );
}
