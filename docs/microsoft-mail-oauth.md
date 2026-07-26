# Microsoft Mail OAuth Lifecycle

This project treats Microsoft Graph and Outlook IMAP as the two Microsoft mail
retrieval channels. Outlook REST v2.0 was decommissioned by Microsoft in March
2024 and is excluded from the provider capability model.

## Shared Refresh Token

A Microsoft refresh token can request access tokens for resources and tenants
where the client and user have already granted permission. The refresh token is
the rotating credential chain; the resulting access tokens remain resource
specific:

- Graph: `https://graph.microsoft.com/Mail.Read offline_access`
- IMAP: `https://outlook.office.com/IMAP.AccessAsUser.All offline_access`

Having a refresh token therefore permits both exchanges to be attempted, but it
does not prove that both grants exist. Graph and IMAP capability status must be
tracked independently.

## Refresh Request

Microsoft exposes token exchange through the identity platform token endpoint:

```http
POST https://login.microsoftonline.com/common/oauth2/v2.0/token
Content-Type: application/x-www-form-urlencoded

client_id=CLIENT_ID&
grant_type=refresh_token&
refresh_token=REFRESH_TOKEN&
scope=RESOURCE_SCOPE
```

A confidential web client also authenticates the client. A successful response
contains an access token, `expires_in`, and may contain a replacement refresh
token. When Microsoft returns a replacement, the project stores it as the new
canonical refresh-token chain before later channel work continues.

## Lifetime And Validation

The token response does not contain `refresh_token_expires_in` or an exact
refresh-token expiration timestamp, and Microsoft publishes no refresh-token
introspection endpoint for this flow. The practical validity check is a token
exchange request.

Microsoft's refresh-token documentation currently lists these defaults:

- 24 hours for single-page applications.
- 24 hours for email one-time-passcode authentication flows.
- 90 days for other scenarios.

These defaults are not an exact per-token expiry promise. Tenant policy, sign-in
frequency, credential changes, user action, administrator action, and risk
events can invalidate a refresh token earlier. The UI therefore reports that
Microsoft did not return an exact expiry and shows the latest exchange result.

## Reauthorization After Expiry

An expired, revoked, malformed, or otherwise unusable refresh token commonly
produces `invalid_grant`; conditional access or an additional sign-in step can
produce `interaction_required`. A new refresh token is obtained by starting a
new interactive authorization-code flow with PKCE and requesting
`offline_access` plus the required Graph and IMAP permissions. After the user
signs in and grants the scopes, the authorization code is redeemed at the token
endpoint and the returned refresh token replaces the expired chain.

## Official References

- [Refresh tokens in the Microsoft identity platform](https://learn.microsoft.com/en-us/entra/identity-platform/refresh-tokens)
- [OAuth 2.0 authorization code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow)
- [Microsoft identity platform error codes](https://learn.microsoft.com/en-us/entra/identity-platform/reference-error-codes)
- [Authenticate IMAP and POP connections using OAuth](https://learn.microsoft.com/en-us/exchange/client-developer/legacy-protocols/how-to-authenticate-an-imap-pop-smtp-application-by-using-oauth)
- [Compare Microsoft Graph and retired Outlook REST endpoints](https://learn.microsoft.com/en-us/outlook/rest/compare-graph)
