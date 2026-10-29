import { api, apiUrl } from "../api.js";
export default function Login() {
  return (
    <div className="state-note">
      <p>Sign in to submit, discuss and vote.</p>
      <a className="button" href={apiUrl("/auth/google")}>
        Sign in with Google
      </a>
    </div>
  );
}
