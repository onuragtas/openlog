<?php

namespace App\Services\Billing;

class PaymentDeclinedException extends \RuntimeException
{
}

class PaymentGateway
{
    public function charge(int $amountCents, string $token): void
    {
        usleep(5000);
        if ($token === 'card_declined') {
            throw new PaymentDeclinedException("demo: payment of {$amountCents} cents declined (caught and report()ed)");
        }
    }
}
