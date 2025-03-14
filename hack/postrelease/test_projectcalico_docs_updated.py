import os
from bs4 import BeautifulSoup
import requests
from versions import RELEASE_STREAM


def test_http_redirects_correctly():
    req = requests.get("http://projectcalico.docs.tigera.io/latest", timeout=60)
    assert req.status_code == 200


def test_latest_releases_redirects_correctly():
    req = requests.get("https://projectcalico.docs.tigera.io/latest/release-notes", timeout=60)
    assert req.status_code == 200

    version = BeautifulSoup(req.content, features="html.parser").find("strong")
    assert version.get_text() == RELEASE_STREAM
