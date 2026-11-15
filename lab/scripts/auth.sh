# Sourced by the lab scripts: sign in to the Sophron API as admin when a
# dashboard password exists (make admin-password). Sets AUTH for curl.
# The password is read from the Secret into memory only.
AUTH=()
_pw=$(kubectl -n sophron-system get secret sophron-admin -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true)
if [[ -n "$_pw" ]]; then
  AUTH=(-u "admin:$_pw" -H "X-Sophron: 1")
fi
unset _pw
