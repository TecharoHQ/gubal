# The great browser version archive of Gubal

Gubal is test infrastructure for [Anubis](https://github.com/TecharoHQ/anubis). This spawns many instances of Chrome/Firefox in a Kubernetes cluster so that basic functionality can be confirmed with a large array of browsers. This has to be this complicated because of horrible circumstances involving [Ukranian smart TVs](https://github.com/TecharoHQ/anubis/issues/1728) and other cases where users won't or can't update their browser.

The main way Gubal is intended to be invoked is as part of the code review process in Anubis. In order to invoke Gubal, make a PR comment starting with the string `/gubaltest` in any Anubis PR. This will spawn many common Chrome and Firefox versions to smoke test the version of Anubis associated with that PR.

Lifecycle status: proof of concept. This needs a lot of work to become production-ready. I am making this as a basic proof of concept so I can unblock shipping Anubis.
