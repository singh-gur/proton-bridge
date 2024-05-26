Feature: user's contact
  Background:
    Given there exists an account with username "[user:user]" and password "password"
    And user "[user:user]" has contact "SuperTester@proton.me" with name "Super TESTER"
    And user "[user:user]" has contacts:
      | name          | email             | format | scheme | signature | encryption |
      | Tester One    | tester1@proton.me | plain  | MIME   | enabled   | enabled    |
      | Tester Two    | tester2@proton.me | HTML   | inline | disabled  | disabled   |
    Then it succeeds
    When bridge starts
    And the user logs in with username "[user:user]" and password "password"
    Then it succeeds


  # Implement contacts on black
  @skip-black
  Scenario: Playing with contact settings
    When the contact "SuperTester@proton.me" of user "[user:user]" has message format "plain"
    When the contact "SuperTester@proton.me" of user "[user:user]" has message format "HTML"
    When the contact "SuperTester@proton.me" of user "[user:user]" has encryption scheme "inline"
    When the contact "SuperTester@proton.me" of user "[user:user]" has encryption scheme "MIME"
    When the contact "SuperTester@proton.me" of user "[user:user]" has no signature
    When the contact "SuperTester@proton.me" of user "[user:user]" has no encryption
    When the contact "SuperTester@proton.me" of user "[user:user]" has signature "enabled"
    When the contact "SuperTester@proton.me" of user "[user:user]" has encryption "enabled"
    When the contact "SuperTester@proton.me" of user "[user:user]" has signature "disabled"
    When the contact "SuperTester@proton.me" of user "[user:user]" has encryption "disabled"
    When the contact "SuperTester@proton.me" of user "[user:user]" has public key from file "testdata/keys/pubkey.asc"
    When the contact "SuperTester@proton.me" of user "[user:user]" has public key:
    """
    -----BEGIN PGP PUBLIC KEY BLOCK-----

    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
    =9sH1
    -----END PGP PUBLIC KEY BLOCK-----
    """

