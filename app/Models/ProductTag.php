<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;
use Illuminate\Database\Eloquent\Relations\HasMany;

class ProductTag extends Model
{
    protected $table = 'product_tags';

    public function products(): BelongsToMany
    {
        return $this->belongsToMany(Product::class);
    }

    public function productTagTranslations(): HasMany
    {
        return $this->hasMany(ProductTagTranslation::class);
    }
}
