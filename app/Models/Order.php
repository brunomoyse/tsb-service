<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\HasOne;

class Order extends Model
{
    protected $table = 'orders';

    public function products(): HasMany
    {
        return $this->hasMany(Product::class);
    }

    public function user(): HasOne
    {
        return $this->hasOne(User::class);
    }
}
