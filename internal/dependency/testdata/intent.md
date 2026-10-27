A body declaration:

Depends-On: https://github.com/acme/api/pull/1

These are examples, not declarations:
```text
Depends-On: malformed
<!-- not an HTML comment in a code fence
```
~~~~
Depends-On: malformed
~~~
Depends-On: malformed
~~~~
    Depends-On: malformed
	Depends-On: malformed
> Depends-On: malformed
   > Depends-On: malformed
<!--
Depends-On: malformed
```
-->
<!-- Depends-On: malformed -->
```text
Text Depends-On: malformed
depends-on: malformed
Depends-on: malformed
```

```html <!-- not an HTML comment
Depends-On: malformed
```
   Depends-On: https://github.com/acme/api/pull/2

A trailer-shaped block in the authoritative body is also supported:
Signed-off-by: A Writer <writer@example.com>

Depends-On: https://github.com/acme/api/pull/3
